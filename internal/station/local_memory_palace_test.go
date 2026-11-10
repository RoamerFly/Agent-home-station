package station

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/companion"
)

func TestLocalMemoryPalaceOwnerScopeAndSources(t *testing.T) {
	f := localChatFixture(t)
	const path = "/api/local/memory/palace"
	anonymous, err := http.Get(f.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous palace status: %d", anonymous.StatusCode)
	}
	store, in, err := f.handler.chat.conversation()
	if err != nil {
		t.Fatal(err)
	}
	save := func(input companion.Input, trace, words string) companion.MemoryEntry {
		t.Helper()
		input.TraceID, input.Text = trace, words
		scope := companion.ScopeID(input.Channel, input.Account, input.Thread)
		if err := store.Observe(input, localConversationConfig(""), time.Now()); err != nil {
			t.Fatal(err)
		}
		page, err := store.SearchMemory(t.Context(), companion.MemoryQuery{Scopes: []string{scope}, Originals: true})
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("source lookup: %+v, %v", page, err)
		}
		memory, err := store.SaveMemory(t.Context(), companion.MemoryEntry{
			Scope: scope, Subject: input.Sender, Kind: "preference", Detail: words,
			Sources: []companion.MemorySource{{EventID: page.Items[0].Event.ID, Quote: words}},
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return memory
	}
	own := save(in, "palace-source", "我喜欢在夜海看星星。")
	in.Thread = "other-owner"
	other := save(in, "other-source", "另一段会话的合成原话。")
	code, page := f.request("GET", path+"?kind=preference", nil, true)
	if code != http.StatusOK || len(page["items"].([]any)) != 1 || page["items"].([]any)[0].(map[string]any)["id"] != own.ID {
		t.Fatalf("palace list: %d %#v", code, page)
	}
	code, detail := f.request("GET", path+"/"+own.ID, nil, true)
	if code != http.StatusOK || len(detail["sources"].([]any)) != 1 || detail["sources"].([]any)[0].(map[string]any)["quote"] != own.Detail {
		t.Fatalf("palace detail: %d %#v", code, detail)
	}
	if code, out := f.request("GET", path+"/"+other.ID, nil, true); code != http.StatusNotFound {
		t.Fatalf("other conversation: %d %#v", code, out)
	}
	if code, out := f.request("GET", path+"?kind=preference&cursor=changed", nil, true); code != http.StatusConflict {
		t.Fatalf("invalid cursor: %d %#v", code, out)
	}
	if code, out := f.request("GET", path+"?kind=invalid", nil, true); code != http.StatusBadRequest {
		t.Fatalf("invalid kind: %d %#v", code, out)
	}
	if !strings.Contains(detail["detail"].(string), "夜海") {
		t.Fatal("palace detail lost the original text")
	}
}
