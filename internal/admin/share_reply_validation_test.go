package admin

import (
	"context"
	"encoding/json"
	"testing"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/linuxnative"
)

type syntheticShareReplyModel struct{}

func (syntheticShareReplyModel) Chat(context.Context, string, []map[string]string) (string, error) {
	return `{"action":"reply","messages":["Alpha，晚安","这是合成分享的补充说明"]}`, nil
}

func TestManualShareFinalSubmissionValidatesEveryBubbleAndExactPayload(t *testing.T) {
	for _, change := range []string{"none", "second-proof", "forget", "permission", "payload", "delivery-kind"} {
		t.Run(change, func(t *testing.T) {
			f, p, n, _ := explicitConversationFixture(t)
			observeOutboundSource(t, f, p, "请根据当前分享的实际观察，发送两条完整的合成说明")
			input := p.outboundInput(p.trace.Thread)
			engine, err := companion.New(f.manager.Config().Companion, f.manager.memory, syntheticShareReplyModel{}, "synthetic-share")
			if err != nil {
				t.Fatal(err)
			}
			decision, err := engine.Respond(context.Background(), input)
			if err != nil || len(decision.ReplyContexts) != 2 {
				t.Fatal("production decision missing per-bubble proof", decision, err)
			}
			request := linuxnative.ToolSend{ThreadID: input.Thread, Kind: "text", Text: decision.Messages[0] + "\n" + decision.Messages[1], IdempotencyKey: "synthetic-manual-share"}
			switch change {
			case "second-proof":
				decision.ReplyContexts[1].ContextVersion = "tampered-second-bubble"
			case "forget":
				if err := f.manager.memory.Clear(companion.ScopeID(input.Channel, input.Account, input.Thread)); err != nil {
					t.Fatal(err)
				}
			case "permission":
				f.manager.mu.Lock()
				f.manager.config.Companion.Permissions.ReplyText = false
				f.manager.mu.Unlock()
			case "payload":
				request.Text = "different unreviewed content"
			case "delivery-kind":
				input.ConversationDelivery = false
			}
			_, err = f.manager.submitReviewedShare(context.Background(), n, engine, input, decision, request)
			if change == "none" {
				if err != nil || len(n.requests) != 1 || n.requests[0].Text != request.Text {
					t.Fatal("valid explicit conversation delivery was rejected", err, n.requests)
				}
			} else if err == nil || len(n.requests) != 0 {
				t.Fatal("invalid final share text reached native submit", change, err, n.requests)
			}
		})
	}
}

func TestManualLiteralTextEndpointHasNoAgentAuthenticationBypass(t *testing.T) {
	f, p := trustedAgent(t)
	n := &friendReminderNative{fakeTools: &fakeTools{fakeNative: f.native}}
	f.manager.native = n
	request := map[string]string{"thread_id": p.trace.Thread, "kind": "text", "text": "管理员逐字输入", "idempotency_key": "human-literal"}
	if code, _, _ := f.request("POST", "/api/tools/send", request, false, false); code != 401 {
		t.Fatal("GUI send accepted without its admin session", code)
	}
	f.login()
	if code, _, _ := f.request("POST", "/api/tools/send", request, true, false); code != 403 {
		t.Fatal("GUI send accepted without its CSRF grant", code)
	}
	raw, _ := json.Marshal(request)
	_, _ = p.Call(context.Background(), "/api/tools/send", raw)
	if len(n.requests) != 0 {
		t.Fatal("Agent tool dispatch aliased the protected GUI route", n.requests)
	}
}
