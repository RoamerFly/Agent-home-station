package stationbrowser

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCreatorWorksRejectsMissingStatusFieldsInsteadOfDroppingWork(t *testing.T) {
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
		if _, err := creatorWorksPage(raw); !errors.Is(err, ErrCreatorWorksUnavailable) {
			t.Fatalf("missing %s silently dropped the work: %v", missing, err)
		}
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
