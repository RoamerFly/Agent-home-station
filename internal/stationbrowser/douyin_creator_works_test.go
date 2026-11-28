package stationbrowser

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCreatorWorksKeepsMissingStatusFieldsAsUnknown(t *testing.T) {
	const id = "7391234567890123456"
	for _, missing := range []string{"is_private", "in_reviewing", "is_delete", "is_prohibited"} {
		status := map[string]any{"aweme_id": id, "is_private": false, "private_status": 0,
			"in_reviewing": false, "is_delete": false, "is_prohibited": false}
		delete(status, missing)
		raw, err := json.Marshal(map[string]any{"status_code": 0, "has_more": false, "aweme_list": []any{
			map[string]any{"aweme_id": id, "author": map[string]any{"uid": "123456789012345678"}, "status": status},
		}})
		if err != nil {
			t.Fatal(err)
		}
		page, err := creatorWorksPage(raw)
		if err != nil || len(page.Works) != 1 || page.Works[0].ID != id ||
			page.Works[0].Visibility != "待核对" && missing != "in_reviewing" ||
			page.Works[0].ReviewStatus != "unknown" || !page.Works[0].Reviewing {
			t.Fatalf("missing %s did not retain an ineligible unknown work: %+v %v", missing, page, err)
		}
	}
}

func TestCreatorWorksJoinsItemAndAwemeListWithoutLosingOwnerProof(t *testing.T) {
	const id = "7391234567890123456"
	build := func(owner any) []byte {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"status_code": 0, "has_more": false, "max_cursor": "1795702565",
			"items":      []any{map[string]any{"id": id, "description": "合成作品正文", "create_time": 1795702565, "author_user_id": owner}},
			"aweme_list": []any{map[string]any{"item_id": id, "duration": 45000}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	page, err := creatorWorksPage(build("123456789012345678"))
	if err != nil || len(page.Works) != 1 || page.Works[0].ID != id || page.Works[0].Title != "合成作品正文" ||
		page.Works[0].Caption != "合成作品正文" || !page.Works[0].Video || !page.Works[0].Reviewing ||
		page.Works[0].ReviewStatus != "unknown" || page.Account == "" || page.MaxCursor != "1795702565" ||
		len(page.PageIDs) != 1 || page.PageIDs[0] != id {
		t.Fatalf("joined creator page: %+v %v", page, err)
	}
	if _, err := creatorWorksPage(build(nil)); !errors.Is(err, ErrCreatorWorksOwnerUnverified) {
		t.Fatalf("unproven owner was accepted: %v", err)
	}
}

func TestCreatorWorksAcceptsRichBoundedFirstPage(t *testing.T) {
	const id = "7391234567890123456"
	item := map[string]any{
		"aweme_id": id,
		"author":   map[string]any{"uid": "123456789012345678"},
		"status": map[string]any{"aweme_id": id, "is_private": false, "private_status": 0,
			"in_reviewing": false, "is_delete": false, "is_prohibited": false},
		"media_variants": strings.Repeat("x", 300<<10),
	}
	raw, err := json.Marshal(map[string]any{"status_code": 0, "has_more": false, "aweme_list": []any{item}})
	if err != nil || len(raw) <= 256<<10 {
		t.Fatalf("synthetic page did not exceed former 256 KiB limit: %d %v", len(raw), err)
	}
	page, err := creatorWorksPage(raw)
	if err != nil || len(page.Works) != 1 || page.Works[0].ID != id || page.Account == "" || !page.CoverageKnown {
		t.Fatalf("rich first page lost verified work: %+v %v", page, err)
	}
	item["media_variants"] = strings.Repeat("x", creatorWorksResponseMax)
	raw, err = json.Marshal(map[string]any{"status_code": 0, "has_more": false, "aweme_list": []any{item}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creatorWorksPage(raw); !errors.Is(err, ErrCreatorWorksResponseTooLarge) {
		t.Fatalf("oversized page was not bounded explicitly: %v", err)
	}
}

func TestCreatorWorksOnlyExcludesExplicitDeletedOrProhibited(t *testing.T) {
	const id = "7391234567890123456"
	for _, excluded := range []string{"is_delete", "is_prohibited"} {
		status := map[string]any{"aweme_id": id, "is_private": false, "private_status": 0,
			"in_reviewing": false, "is_delete": false, "is_prohibited": false}
		status[excluded] = true
		raw, err := json.Marshal(map[string]any{"status_code": 0, "has_more": false, "aweme_list": []any{
			map[string]any{"aweme_id": id, "author": map[string]any{"uid": "123456789012345678"}, "status": status},
		}})
		if err != nil {
			t.Fatal(err)
		}
		page, err := creatorWorksPage(raw)
		if err != nil || len(page.Works) != 0 || page.Account == "" {
			t.Fatalf("explicit %s should retain the verified owner while excluding the work: %+v %v", excluded, page, err)
		}
	}
}
