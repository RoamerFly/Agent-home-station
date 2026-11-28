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

var (
	ErrCreatorWorksUnavailable      = errors.New("创作者中心未返回可核对的本人作品")
	ErrCreatorWorksChanged          = errors.New("创作者账号或前一页作品已变化，请从首页重查")
	ErrCreatorWorksAccountChanged   = errors.New("创作者中心账号已变化，请重新核对登录")
	ErrCreatorWorksNextUnavailable  = errors.New("创作者中心报告还有作品，但当前页面未提供可核对的下一页入口")
	ErrCreatorWorksResponseTooLarge = errors.New("创作者作品列表响应超过本机 8 MiB 安全读取范围，本页未保存")
)

const creatorWorksResponseMax = 8 << 20

type DouyinCreatorWork struct {
	ID, Title, Caption, CreatedAt, Visibility string
	Reviewing, Video                          bool
	// Images contains the ordered image candidates from the creator
	// response. These CDN URLs are private to the host.
	Images []string
}

type DouyinCreatorWorksPage struct {
	// Account and PageKey are private proof anchors, never user-visible account IDs.
	Account, PageKey       string
	Works                  []DouyinCreatorWork
	HasMore, CoverageKnown bool
}

type DouyinCreatorWorksRequest struct {
	Page                             int
	PreviousAccount, PreviousPageKey string
}

// Later pages currently require clicking through the preceding pages from a
// fresh, account-bound browser session. Leave headroom under the local HTTP
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
		StatusCode *int            `json:"status_code"`
		HasMore    json.RawMessage `json:"has_more"`
		Works      *[]struct {
			ID          string          `json:"aweme_id"`
			Title       string          `json:"item_title"`
			Caption     string          `json:"caption"`
			Description string          `json:"desc"`
			Created     int64           `json:"create_time"`
			Video       json.RawMessage `json:"video"`
			Images      []struct {
				URLs []string `json:"url_list"`
			} `json:"images"`
			Author struct {
				UID string `json:"uid"`
			} `json:"author"`
			Status struct {
				ID            string `json:"aweme_id"`
				Private       *bool  `json:"is_private"`
				PrivateStatus *int   `json:"private_status"`
				Reviewing     *bool  `json:"in_reviewing"`
				Deleted       *bool  `json:"is_delete"`
				Prohibited    *bool  `json:"is_prohibited"`
			} `json:"status"`
		} `json:"aweme_list"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.StatusCode == nil || *envelope.StatusCode != 0 || envelope.Works == nil || len(*envelope.Works) > 1000 {
		return result, ErrCreatorWorksUnavailable
	}
	switch string(envelope.HasMore) {
	case "true", "1":
		result.HasMore, result.CoverageKnown = true, true
	case "false", "0":
		result.CoverageKnown = true
	}
	// An empty final page is usable only after the browser has revalidated a
	// preceding page in this same session. The parser itself cannot attest the
	// account of a zero-work first page, so it leaves Account empty.
	if len(*envelope.Works) == 0 && (!result.CoverageKnown || result.HasMore) {
		return result, ErrCreatorWorksUnavailable
	}
	owner := ""
	seen := map[string]bool{}
	keys := make([]string, 0, len(*envelope.Works))
	for _, item := range *envelope.Works {
		if !creatorWorksNumericID.MatchString(item.ID) || item.Status.ID != item.ID || !creatorWorksNumericID.MatchString(item.Author.UID) ||
			owner != "" && owner != item.Author.UID || seen[item.ID] {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
		}
		owner, seen[item.ID] = item.Author.UID, true
		keys = append(keys, item.ID)
		status := item.Status
		if status.Deleted == nil || status.Prohibited == nil || status.Reviewing == nil || status.Private == nil {
			return DouyinCreatorWorksPage{}, ErrCreatorWorksUnavailable
		}
		if *status.Deleted || *status.Prohibited {
			continue
		}
		visibility := "待核对"
		if status.PrivateStatus != nil {
			switch {
			case *status.Private && *status.PrivateStatus == 1:
				visibility = "仅自己可见"
			case !*status.Private && *status.PrivateStatus == 0:
				visibility = "公开"
			}
		}
		title := strings.TrimSpace(item.Title)
		caption := strings.TrimSpace(item.Caption)
		if caption == "" {
			caption = strings.TrimSpace(item.Description)
		}
		created := ""
		if item.Created > 0 && item.Created < time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
			created = time.Unix(item.Created, 0).UTC().Format(time.RFC3339)
		}
		var video struct {
			Duration int64 `json:"duration"`
		}
		videoConfirmed := json.Unmarshal(item.Video, &video) == nil && video.Duration > 0
		work := DouyinCreatorWork{ID: item.ID, Title: title, Caption: caption, CreatedAt: created,
			Visibility: visibility, Reviewing: *status.Reviewing, Video: videoConfirmed}
		if visibility == "公开" && !work.Reviewing && !videoConfirmed {
			for _, image := range item.Images {
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
	result.PageKey = creatorWorksDigest(strings.Join(keys, ","))
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
