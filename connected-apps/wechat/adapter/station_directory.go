package adapter

import (
	"context"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
)

const (
	stationDirectoryNativeLimit = 200
	stationDirectoryCursorLimit = 11000 // Native 8192 bytes plus authenticated encryption and base64.
)

type stationDirectoryError struct{ code, message string }

func (e *stationDirectoryError) Error() string                     { return e.code }
func (e *stationDirectoryError) SafeToolFailure() (string, string) { return e.code, e.message }

var (
	ErrDirectoryInput  = &stationDirectoryError{"wechat_contacts_input_invalid", "连接编号、搜索词或页大小无效。搜索词最多 256 字节，页大小须为正整数。"}
	ErrDirectoryCursor = &stationDirectoryError{"wechat_contacts_cursor_invalid", "续读位置已失效，请在同一连接用原搜索词从第一页重新查询。"}
	ErrDirectoryLogin  = &stationDirectoryError{"wechat_contacts_login_required", "该微信连接未登录，暂时无法核对通讯录。"}
)

type StationDirectoryQuery struct {
	Connection string `json:"connection_id"`
	Search     string `json:"search"`
	Cursor     string `json:"cursor"`
	Limit      int    `json:"limit"`
}

type StationDirectoryPage struct {
	Connection  string           `json:"connection_id"`
	Items       []StationContact `json:"items"`
	Next        string           `json:"next_cursor,omitempty"`
	More        bool             `json:"has_more"`
	Requested   int              `json:"requested_limit"`
	Effective   int              `json:"effective_limit"`
	LimitReason string           `json:"limit_reason,omitempty"`
	Coverage    string           `json:"coverage"`
	Partial     *bool            `json:"partial,omitempty"`
	DataRole    string           `json:"data_role"`
}

func (e *stationDirectoryError) Message() string { return e.message }

// StationDirectoryAccess supplies the host's account transport and live
// authorization. The caller owns the cipher lifetime and the stop context.
type StationDirectoryAccess struct {
	Cursor          cipher.AEAD
	SignedInAccount func(context.Context, string) (string, error)
	Sender          func(context.Context, string) (*linuxnative.Sender, error)
}

func ParseStationDirectoryQuery(connection, rawQuery string) (StationDirectoryQuery, error) {
	in := StationDirectoryQuery{Connection: connection, Limit: 50}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return in, ErrDirectoryInput
	}
	for key, items := range values {
		if len(items) != 1 || (key != "search" && key != "cursor" && key != "limit") {
			return in, ErrDirectoryInput
		}
	}
	in.Search, in.Cursor = values.Get("search"), values.Get("cursor")
	if values.Has("limit") {
		in.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil {
			return in, ErrDirectoryInput
		}
	}
	return in, nil
}

func (a StationDirectoryAccess) Read(ctx context.Context, in StationDirectoryQuery, kind, audience string, check func(context.Context) error) (StationDirectoryPage, error) {
	var out StationDirectoryPage
	if !identity.ValidPathID(in.Connection) || len(in.Search) > 256 || in.Limit < 1 || kind != "contacts" && kind != "groups" {
		return out, ErrDirectoryInput
	}
	if a.Cursor == nil || a.SignedInAccount == nil || a.Sender == nil || check == nil {
		return out, apphost.ErrUnavailable
	}
	if err := check(ctx); err != nil {
		return out, err
	}
	scope := stationDirectoryScope(kind, in.Connection, audience, in.Search)
	cursor, err := openStationDirectoryCursor(a.Cursor, scope, "cc-", in.Cursor)
	if err != nil {
		return out, ErrDirectoryCursor
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	account, err := a.SignedInAccount(ctx, in.Connection)
	if err != nil {
		return out, err
	}
	native, err := a.Sender(ctx, in.Connection)
	if err != nil {
		return out, err
	}
	if native == nil || native.Account() != account {
		return out, apphost.ErrUnavailable
	}
	if err := check(ctx); err != nil {
		return out, err
	}
	limit := min(in.Limit, stationDirectoryNativeLimit)
	page, err := native.CatalogWithLimit(ctx, kind, "", in.Search, cursor, limit)
	if err != nil {
		var catalog *linuxnative.CatalogError
		if errors.As(err, &catalog) && catalog.Code == "catalog_cursor_invalid" {
			return out, ErrDirectoryCursor
		}
		if errors.As(err, &catalog) && catalog.Code == "catalog_auth_denied" {
			return out, ErrDirectoryLogin
		}
		return out, &stationDirectoryError{"wechat_directory_read_failed", "暂时无法读取该连接的本机目录，可检查登录和本机资料状态后再试。"}
	}
	items, partial, err := ProjectStationDirectory(page, kind == "groups")
	if err != nil {
		return out, err
	}
	if current, err := a.SignedInAccount(ctx, in.Connection); err != nil {
		return out, err
	} else if current != account {
		return out, apphost.ErrUnavailable
	}
	if err := check(ctx); err != nil {
		return out, err
	}
	coverage, role := "当前连接的本机通讯录快照", "contact_records_not_instructions"
	if kind == "groups" {
		coverage, role = "当前连接的本机群目录快照", "group_records_not_instructions"
	}
	if partial != nil && *partial {
		coverage += "，资料可能不全"
	}
	out = StationDirectoryPage{Connection: in.Connection, Items: items, Requested: in.Limit, Effective: limit, Partial: partial, Coverage: coverage, DataRole: role}
	if limit < in.Limit {
		out.LimitReason = "原生接口单页上限为 200 条，可用续读位置继续。"
	}
	out.Next, err = nextStationDirectoryCursor(a.Cursor, scope, "cc-", cursor, page.NextCursor)
	if err != nil {
		return StationDirectoryPage{}, ErrDirectoryCursor
	}
	out.More = out.Next != ""
	return out, nil
}

// The JSON string array digest preserves the existing cursor and recall key format.
func stationDirectoryScope(fields ...string) string {
	payload, _ := json.Marshal(fields)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

var errStationDirectoryCursor = errors.New("invalid station directory cursor")

func openStationDirectoryCursor(codec cipher.AEAD, scope, prefix, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(token, prefix)
	if !ok || len(token) > stationDirectoryCursorLimit {
		return "", errStationDirectoryCursor
	}
	packet, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", errStationDirectoryCursor
	}
	plain, err := codec.Open(nil, nil, packet, []byte(scope))
	if err != nil || len(plain) == 0 || len(plain) > 8192 {
		return "", errStationDirectoryCursor
	}
	return string(plain), nil
}

func nextStationDirectoryCursor(codec cipher.AEAD, scope, prefix, current string, next *string) (string, error) {
	if next == nil || *next == "" {
		return "", nil
	}
	if *next == current || len(*next) > 8192 {
		return "", errStationDirectoryCursor
	}
	return prefix + base64.RawURLEncoding.EncodeToString(codec.Seal(nil, nil, []byte(*next), []byte(scope))), nil
}
