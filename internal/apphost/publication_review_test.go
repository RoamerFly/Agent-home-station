package apphost

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPublicationReviewIsExactOwnerTaskAndSeparateFromAgentStatus(t *testing.T) {
	publisher, _, _, target := newPublicationFixture(t)
	actor := publicationActor("review-original")
	draft := publicationDraft(target)
	draft.Title, draft.Body = "审阅标题", "审阅正文"
	draft.Options = json.RawMessage(`{"visibility":"private"}`)
	created, err := publisher.Draft(t.Context(), actor, draft)
	if err != nil {
		t.Fatal(err)
	}
	review, err := publisher.ReviewForOwner(t.Context(), actor, "social", created.JobID)
	if err != nil || review.Task.State != "draft" || review.Task.ManifestSHA256 != created.ManifestSHA256 ||
		review.Title != draft.Title || review.Body != draft.Body || len(review.Artifacts) != 1 || review.Artifacts[0] != publicationArtifact() ||
		string(review.Options) != string(draft.Options) {
		t.Fatalf("owner review: %#v %v", review, err)
	}
	raw, err := json.Marshal(review)
	if err != nil || strings.Contains(string(raw), "origin") || strings.Contains(string(raw), "platform_options") {
		t.Fatalf("review serialized private provenance or provider options: %s %v", raw, err)
	}
	listed, err := publisher.List(t.Context(), actor, "social", 0, 10)
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("agent task list: %#v %v", listed, err)
	}
	listedRaw, _ := json.Marshal(listed)
	if strings.Contains(string(listedRaw), draft.Title) || strings.Contains(string(listedRaw), draft.Body) || strings.Contains(string(listedRaw), publicationArtifact().Name) {
		t.Fatalf("agent list leaked owner review: %s", listedRaw)
	}
	other := actor
	other.Requester = "different-owner"
	if _, err := publisher.ReviewForOwner(t.Context(), other, "social", created.JobID); !errors.Is(err, ErrPublicationOwnership) {
		t.Fatalf("different actor read review: %v", err)
	}
	if _, err := publisher.ReviewForOwner(t.Context(), actor, "douyin", created.JobID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("different app read review: %v", err)
	}
}
