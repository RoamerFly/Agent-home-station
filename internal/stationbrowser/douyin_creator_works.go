package stationbrowser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const DouyinCreatorManageURL = "https://creator.douyin.com/creator-micro/content/manage"

var creatorWorksNumericID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var creatorWorksCursorNumber = regexp.MustCompile(`^[0-9]{1,20}$`)

var (
	ErrCreatorWorksUnavailable      = errors.New("创作者中心未返回可核对的本人作品")
	ErrCreatorWorksChanged          = errors.New("创作者账号或前一页作品已变化，请从首页重查")
	ErrCreatorWorksAccountChanged   = errors.New("创作者中心账号已变化，请重新核对登录")
	ErrCreatorWorksNextUnavailable  = errors.New("创作者中心报告还有作品，但当前页面未提供可核对的下一页入口")
	ErrCreatorWorksResponseTooLarge = errors.New("创作者作品列表响应超过本机 8 MiB 安全读取范围，本页未保存")
	ErrCreatorWorksOwnerUnverified  = errors.New("创作者作品列表缺少作者 UID，无法核对是否属于当前账号；请在创作者中心打开作品管理页并刷新后重试")
)

const creatorWorksResponseMax = 8 << 20

type DouyinCreatorWork struct {
	ID, Title, Caption, CreatedAt, Visibility string
	// ReviewStatus is unknown when the response omits the moderation or
	// deletion flags. Such a work remains visible in the catalog but cannot
	// be used as proof of a published, interactive work.
	ReviewStatus     string
	Reviewing, Video bool
	// Images contains the ordered image candidates from the creator
	// response. These CDN URLs are private to the host.
	Images []string
}

type creatorWorksStatus struct {
	ID            json.RawMessage `json:"aweme_id"`
	Private       *bool           `json:"is_private"`
	PrivateStatus *int            `json:"private_status"`
	Reviewing     *bool           `json:"in_reviewing"`
	Deleted       *bool           `json:"is_delete"`
	Prohibited    *bool           `json:"is_prohibited"`
}

type creatorWorksRecord struct {
	ID          json.RawMessage `json:"id"`
	ItemID      json.RawMessage `json:"item_id"`
	AwemeID     json.RawMessage `json:"aweme_id"`
	Title       string          `json:"item_title"`
	Caption     string          `json:"caption"`
	Description string          `json:"description"`
	Desc        string          `json:"desc"`
	Created     int64           `json:"create_time"`
	Duration    int64           `json:"duration"`
	Video       json.RawMessage `json:"video"`
	Images      []struct {
		URLs []string `json:"url_list"`
	} `json:"images"`
	Author struct {
		UID json.RawMessage `json:"uid"`
	} `json:"author"`
	AuthorUID json.RawMessage     `json:"author_uid"`
	AuthorID  json.RawMessage     `json:"author_user_id"`
	Status    *creatorWorksStatus `json:"status"`
}

func creatorWorksIdentifier(values ...json.RawMessage) (string, bool) {
	id := ""
	for _, raw := range values {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		value := string(raw)
		if raw[0] == '"' && json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		if !creatorWorksNumericID.MatchString(value) || id != "" && id != value {
			return "", false
		}
		id = value
	}
	return id, true
}

func (record creatorWorksRecord) workID(fromItems bool) (string, bool) {
	// In items[] the canonical key is id; in aweme_list[] it is aweme_id.
	// The old numeric item_id alias can lose low digits in browser-side
	// conversions and is used only if neither canonical key is present.
	if fromItems && len(record.ID) != 0 && string(record.ID) != "null" {
		return creatorWorksIdentifier(record.ID)
	}
	if len(record.AwemeID) != 0 && string(record.AwemeID) != "null" {
		return creatorWorksIdentifier(record.AwemeID)
	}
	if len(record.ID) != 0 && string(record.ID) != "null" {
		return creatorWorksIdentifier(record.ID)
	}
	return creatorWorksIdentifier(record.ItemID)
}

func creatorWorksCursor(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	value := string(raw)
	if raw[0] == '"' && json.Unmarshal(raw, &value) != nil {
		return ""
	}
	if !creatorWorksCursorNumber.MatchString(value) {
		return ""
	}
	return value
}

type DouyinCreatorWorksPage struct {
	// Account and PageKey are private proof anchors, never user-visible account IDs.
	Account, PageKey string
	// PageIDs includes explicitly excluded records so successive platform
	// pages can be checked for any overlap, not only identical whole pages.
	PageIDs []string
	// MaxCursor is populated only when the observed response contains a
	// valid decimal next-page cursor; it is never inferred from work IDs.
	MaxCursor              string
	Works                  []DouyinCreatorWork
	HasMore, CoverageKnown bool
}

type DouyinCreatorWorksRequest struct {
	Page                             int
	PreviousAccount, PreviousPageKey string
}

// Later pages require traversing preceding pages in a fresh, account-bound
// browser session. Leave headroom under the local HTTP
// route's three-minute deadline for authorization and result persistence.
func DouyinCreatorWorksTimeout(page int) time.Duration {
	const base = 45 * time.Second
	const perPage = 10 * time.Second
	const maximum = 165 * time.Second
	if page <= 0 {
		return base
	}
	if page >= int((maximum-base)/perPage) {
		return maximum
	}
	return base + time.Duration(page)*perPage
}

func creatorWorksPage(raw []byte) (DouyinCreatorWorksPage, error) {
	var result DouyinCreatorWorksPage
	if len(raw) > creatorWorksResponseMax {
		return result, ErrCreatorWorksResponseTooLarge
	}
	var envelope struct {
		StatusCode *int                  `json:"status_code"`
		HasMore    json.RawMessage       `json:"has_more"`
		MaxCursor  json.RawMessage       `json:"max_cursor"`
		Items      *[]creatorWorksRecord `json:"items"`
		Works      *[]creatorWorksRecord `json:"aweme_list"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.StatusCode == nil || *envelope.StatusCode != 0 ||
		envelope.Items == nil && envelope.Works == nil {
		return result, ErrCreatorWorksUnavailable
	}
	items, awemes := []creatorWorksRecord{}, []creatorWorksRecord{}
	if envelope.Items != nil {
		items = *envelope.Items
	}
	if envelope.Works != nil {
		awemes = *envelope.Works
	}
	if len(items) > 1000 || len(awemes) > 1000 {
		return result, ErrCreatorWorksUnavailable
	}
	result.MaxCursor = creatorWorksCursor(envelope.MaxCursor)
	switch string(envelope.HasMore) {
	case "true", "1":
		result.HasMore, result.CoverageKnown = true, true
	case "false", "0":
		result.CoverageKnown = true
	}
	// An empty final page is usable only after the browser has revalidated a
	// preceding page in this same session. The parser itself cannot attest the
	// account of a zero-work first page, so it leaves Account empty.
	if len(items) == 0 && len(awemes) == 0 && (!result.CoverageKnown || result.HasMore) {
		return result, ErrCreatorWorksUnavailable
	}
	type pair struct{ item, aweme *creatorWorksRecord }
	byID := make(map[string]*pair, len(items)+len(awemes))
	order := make([]string, 0, len(items)+len(awemes))
	for i := range items {
		id, ok := items[i].workID(true)
		if !ok || id == "" || byID[id] != nil {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
		}
		byID[id] = &pair{item: &items[i]}
		order = append(order, id)
	}
	for i := range awemes {
		id, ok := awemes[i].workID(false)
		if !ok || id == "" {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
		}
		p := byID[id]
		if p == nil {
			p = &pair{}
			byID[id] = p
			order = append(order, id)
		} else if p.aweme != nil {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
		}
		p.aweme = &awemes[i]
	}
	result.PageIDs = append(result.PageIDs, order...)
	owner := ""
	for _, id := range order {
		p := byID[id]
		item, aweme := p.item, p.aweme
		uid := ""
		for _, record := range []*creatorWorksRecord{item, aweme} {
			if record == nil {
				continue
			}
			candidate, ok := creatorWorksIdentifier(record.Author.UID, record.AuthorUID, record.AuthorID)
			if !ok || candidate != "" && uid != "" && candidate != uid {
				return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
			}
			if candidate != "" {
				uid = candidate
			}
			if record.Status != nil {
				statusID, ok := creatorWorksIdentifier(record.Status.ID)
				if !ok || statusID != "" && statusID != id {
					return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
				}
			}
		}
		if uid == "" {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksOwnerUnverified
		}
		if owner != "" && owner != uid {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
		}
		owner = uid
		status := (*creatorWorksStatus)(nil)
		if item != nil {
			status = item.Status
		}
		if aweme != nil && aweme.Status != nil {
			status = aweme.Status
		}
		if status != nil && (status.Deleted != nil && *status.Deleted || status.Prohibited != nil && *status.Prohibited) {
			continue
		}
		visibility := "待核对"
		reviewing, reviewStatus := true, "unknown"
		if status != nil && status.Deleted != nil && status.Prohibited != nil && status.Reviewing != nil && status.Private != nil {
			reviewing = *status.Reviewing
			reviewStatus = "not_reviewing"
			if reviewing {
				reviewStatus = "reviewing"
			}
		}
		if status != nil && status.Private != nil && status.PrivateStatus != nil &&
			status.Deleted != nil && status.Prohibited != nil {
			switch {
			case *status.Private && *status.PrivateStatus == 1:
				visibility = "仅自己可见"
			case !*status.Private && *status.PrivateStatus == 0:
				visibility = "公开"
			}
		}
		metadata := aweme
		if item != nil {
			metadata = item
		}
		title := strings.TrimSpace(metadata.Title)
		caption := strings.TrimSpace(metadata.Caption)
		if caption == "" {
			caption = strings.TrimSpace(metadata.Description)
		}
		if caption == "" {
			caption = strings.TrimSpace(metadata.Desc)
		}
		if title == "" && aweme != nil {
			title = strings.TrimSpace(aweme.Title)
		}
		if caption == "" && aweme != nil {
			caption = strings.TrimSpace(aweme.Caption)
		}
		if title == "" {
			title = caption
		}
		created := ""
		createdAt := metadata.Created
		if createdAt == 0 && aweme != nil {
			createdAt = aweme.Created
		}
		if createdAt > 0 && createdAt < time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
			created = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		}
		var video struct {
			Duration int64 `json:"duration"`
		}
		videoConfirmed := metadata.Duration > 0 || json.Unmarshal(metadata.Video, &video) == nil && video.Duration > 0
		if aweme != nil {
			videoConfirmed = videoConfirmed || aweme.Duration > 0 || json.Unmarshal(aweme.Video, &video) == nil && video.Duration > 0
		}
		work := DouyinCreatorWork{ID: id, Title: title, Caption: caption, CreatedAt: created,
			Visibility: visibility, ReviewStatus: reviewStatus, Reviewing: reviewing, Video: videoConfirmed}
		if visibility == "公开" && !work.Reviewing && !videoConfirmed {
			images := metadata.Images
			if len(images) == 0 && aweme != nil {
				images = aweme.Images
			}
			for _, image := range images {
				url := ""
				for _, candidate := range image.URLs {
					if validDouyinCreatorImageURL(candidate) {
						url = candidate
						break
					}
				}
				if url == "" {
					work.Images = nil
					break
				}
				work.Images = append(work.Images, url)
			}
		}
		result.Works = append(result.Works, work)
	}
	if owner != "" {
		result.Account = creatorWorksDigest(owner)
	}
	result.PageKey = creatorWorksDigest(strings.Join(order, ","))
	return result, nil
}

func anchorCreatorWorksPage(page DouyinCreatorWorksPage, previousAccount string, position int) (DouyinCreatorWorksPage, error) {
	if page.Account != "" {
		return page, nil
	}
	// Only a later, explicit final empty page may inherit the account from
	// preceding nonempty page(s) verified in the same browser session.
	if position <= 0 || previousAccount == "" || !page.CoverageKnown || page.HasMore || page.PageKey == "" || len(page.Works) != 0 {
		return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
	}
	page.Account = previousAccount
	return page, nil
}

func creatorWorksDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
