package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"wechat-bot/internal/apphost"
	"wechat-bot/internal/identity"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/websession"
)

var ErrGroupActionPending = errors.New("previous group action outcome pending")
var ErrGroupActionTarget = errors.New("group target is not verified")
var ErrGroupActionUnsupported = errors.New("group action is not advertised")

// ManagementOwner is captured and revalidated by the host at dispatch.
type ManagementOwner struct {
	ID     string
	Active func() bool
}

// ActionManagement owns the HTTP protocol. The host supplies scoped authority
// and keeps control of account credentials, native clients, and the ledger.
type ActionManagement struct {
	Shutdown     context.Context
	Owner        func(*http.Request) (ManagementOwner, error)
	ApplyGroup   func(context.Context, string, linuxnative.AdminAction, string, ManagementOwner) (apphost.ActionReceipt, error)
	ReadGroup    func(context.Context, string, string, []string) (GroupManagementDetails, error)
	LookupRecall func(context.Context, string, string, string, string) (RecallView, error)
	ApplyRecall  func(context.Context, string, string, string, string, ManagementOwner) (apphost.ActionReceipt, error)
}

func (h ActionManagement) operationContext(parent context.Context, timeout time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(h.Shutdown, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func validManagementConnection(id string) bool {
	hash, ok := strings.CutPrefix(id, ID+"-")
	if !ok || len(hash) != sha256.Size*2 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

type groupActionInput struct {
	Action string  `json:"action"`
	Name   *string `json:"name"`
	Group  string  `json:"group_id"`
	Muted  *bool   `json:"muted"`
	Key    string  `json:"request_key"`
}

func (h ActionManagement) SubmitGroup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in groupActionInput
	if !websession.Decode(w, r, &in) {
		return
	}
	action := linuxnative.AdminAction{Kind: in.Action, ThreadID: in.Group}
	if action.Kind == "" {
		action.Kind = "mute"
	}
	if !ValidGroup(in.Group) || !identity.ValidPathID(in.Key) || !validManagementConnection(r.PathValue("connection")) {
		websession.Fail(w, http.StatusBadRequest, "请提供有效的连接、群 ID 和请求编号")
		return
	}
	switch {
	case action.Kind == "mute" && in.Muted != nil && (in.Name == nil || *in.Name == ""):
		action.Muted = *in.Muted
	case (action.Kind == "rename" || action.Kind == "my_name") && in.Muted == nil &&
		in.Name != nil && ValidGroupName(*in.Name, action.Kind == "my_name"):
		action.Text = *in.Name
	default:
		websession.Fail(w, http.StatusBadRequest, "请提供有效的群操作。群名称须非空，本人群昵称可明确提交空字符串。名称最多 512 UTF-8 字节，且不能含控制字符")
		return
	}
	owner, err := h.Owner(r)
	if err != nil {
		websession.Fail(w, http.StatusConflict, "请先准备本地主人身份")
		return
	}
	ctx, cancel := h.operationContext(r.Context(), 25*time.Second)
	defer cancel()
	out, err := h.ApplyGroup(ctx, r.PathValue("connection"), action, in.Key, owner)
	switch {
	case err == nil:
		websession.Respond(w, http.StatusAccepted, out)
	case errors.Is(err, ErrGroupActionPending):
		websession.Fail(w, http.StatusConflict, "此群已有结果待核对的操作，请先查询原操作")
	case errors.Is(err, ErrGroupActionUnsupported):
		websession.Fail(w, http.StatusConflict, "当前微信后台未声明此群操作")
	case errors.Is(err, ErrGroupActionTarget), errors.Is(err, linuxnative.ErrToolNotFound), errors.Is(err, apphost.ErrNotFound):
		websession.Fail(w, http.StatusNotFound, "未能在该连接核对所选群")
	case errors.Is(err, apphost.ErrLifecycleCutover):
		websession.Fail(w, http.StatusConflict, "旧微信接续未获启用或凭证已失效，群操作未提交")
	case errors.Is(err, apphost.ErrLifecycleConflict):
		websession.Fail(w, http.StatusConflict, "连接已暂停、未登录或请求内容变化，未提交操作")
	default:
		websession.Fail(w, http.StatusServiceUnavailable, "暂时无法核对操作，请查看该群的原操作记录")
	}
}

type RecallView struct {
	MessageID  string                 `json:"message_id,omitempty"`
	NextCursor string                 `json:"next_cursor,omitempty"`
	Operation  *apphost.ActionReceipt `json:"operation,omitempty"`
}

var ErrRecallCursor = errors.New("invalid recall cursor")

func (h ActionManagement) ReadRecall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := h.operationContext(r.Context(), 20*time.Second)
	defer cancel()
	id, key := r.PathValue("connection"), r.PathValue("receipt")
	view, err := h.LookupRecall(ctx, id, key, r.URL.Query().Get("server_id"), r.URL.Query().Get("cursor"))
	if err != nil {
		recallFailure(w, err)
		return
	}
	websession.Respond(w, http.StatusOK, view)
}

func (h ActionManagement) SubmitRecall(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		ServerID  string `json:"server_id"`
		MessageID string `json:"message_id"`
	}
	if !websession.Decode(w, r, &in) {
		return
	}
	if !ValidServerReceiptID(in.ServerID) || !ValidRecallMessageID(in.MessageID) {
		websession.Fail(w, http.StatusBadRequest, "请从投递记录中选择已核对的原消息")
		return
	}
	owner, err := h.Owner(r)
	if err != nil {
		websession.Fail(w, http.StatusConflict, "请先准备本地主人身份")
		return
	}
	ctx, cancel := h.operationContext(r.Context(), 25*time.Second)
	defer cancel()
	result, err := h.ApplyRecall(ctx, r.PathValue("connection"), r.PathValue("receipt"), in.ServerID, in.MessageID, owner)
	if err != nil {
		recallFailure(w, err)
		return
	}
	websession.Respond(w, http.StatusAccepted, result)
}

func recallFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrNotFound):
		websession.Fail(w, http.StatusNotFound, "未找到该连接中匹配的已接受投递")
	case errors.Is(err, ErrRecallCursor):
		websession.Fail(w, http.StatusBadRequest, "此查询位置已失效，请从首批重新查找")
	case errors.Is(err, apphost.ErrLifecycleCutover):
		websession.Fail(w, http.StatusConflict, "旧微信接续未获启用或凭证已失效，撤回未提交")
	case errors.Is(err, apphost.ErrLifecycleConflict):
		websession.Fail(w, http.StatusConflict, "账号未登录、连接已暂停或原消息身份无法核对")
	default:
		websession.Fail(w, http.StatusServiceUnavailable, "暂时无法核对撤回，请查询原操作")
	}
}

func ValidRecallMessageID(id string) bool {
	raw, ok := strings.CutPrefix(id, "msg_")
	if !ok || raw == "" || len(id) > 8192 {
		return false
	}
	for _, c := range raw {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

var ErrGroupDetailsLedger = errors.New("group action records unavailable")

type GroupManagementDetails struct {
	Receipts     map[string]apphost.ActionReceipt
	Profile      *linuxnative.GroupProfile
	Capabilities linuxnative.Capabilities
	CanChange    bool
}

func (h ActionManagement) GroupDetails(kinds ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, group := r.PathValue("connection"), r.PathValue("group")
		if !ValidGroup(group) || !validManagementConnection(id) {
			websession.Fail(w, http.StatusBadRequest, "连接或群编号无效")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		details, err := h.ReadGroup(ctx, id, group, kinds)
		if details.Profile == nil && len(details.Receipts) == 0 {
			switch {
			case errors.Is(err, ErrGroupDetailsLedger):
				websession.Fail(w, http.StatusServiceUnavailable, "暂时无法核对该群操作记录")
			case errors.Is(err, apphost.ErrNotFound), errors.Is(err, linuxnative.ErrToolNotFound):
				websession.Fail(w, http.StatusNotFound, "未能在该连接核对所选群")
			default:
				websession.Fail(w, http.StatusServiceUnavailable, "暂时无法核对所选群资料")
			}
			return
		}
		websession.Respond(w, http.StatusOK, groupDetailsView(details, kinds))
	}
}

func groupDetailsView(details GroupManagementDetails, kinds []string) map[string]any {
	result := map[string]any{"coverage": "unavailable"}
	if details.Profile != nil {
		result["coverage"] = "cached"
	}
	for _, kind := range kinds {
		available, operation := "can_change", "operation"
		switch kind {
		case "rename":
			available = "can_rename"
		case "my_name":
			available, operation = "can_set_my_name", "my_name_operation"
		}
		receipt, found := details.Receipts[kind]
		if found {
			result[operation] = receipt
		}
		pending := receipt.OperationID != "" && !IsFinalActionState(receipt.State)
		result[available] = details.Profile != nil && details.CanChange && !pending && GroupActionSupported(details.Capabilities, kind)
		if profile := details.Profile; profile != nil {
			switch kind {
			case "mute":
				if profile.Muted != nil {
					result["muted"] = *profile.Muted
				}
			case "rename":
				if profile.Name != "" {
					result["name"] = profile.Name
				}
			case "my_name":
				if profile.MyDisplayName != nil {
					result["my_display_name"] = *profile.MyDisplayName
				}
			}
		}
	}
	return result
}
