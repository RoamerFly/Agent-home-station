package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

type privateBrowseBotTools struct {
	t                       *testing.T
	trace                   string
	input                   companion.Input
	ctx                     context.Context
	bound, listed, reviewed int
	trap                    bool
}

func (p *privateBrowseBotTools) ForkTools() companion.ToolAccess { return p }
func (p *privateBrowseBotTools) SetTrace(in companion.Input) {
	if p.trap {
		p.t.Fatal("ordinary account tools inherited by private preview")
	}
	p.input = in
	if !in.Preview || !in.Surf || in.Channel != "host" || in.Thread != "host:surf:"+in.Account || in.TraceID != p.trace || in.Sender != "" || in.SurfForceShare {
		p.t.Fatal("observer forged or transferred source", in)
	}
}
func (p *privateBrowseBotTools) List(context.Context) ([]mcptools.Tool, error) {
	if p.trap {
		p.t.Fatal("ordinary tool catalog accessed by private preview")
	}
	p.listed++
	return []mcptools.Tool{{Name: "web_fetch", InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)}}, nil
}
func (p *privateBrowseBotTools) Call(context.Context, string, json.RawMessage) (string, error) {
	p.t.Fatal("unexpected tool execution in lifecycle test")
	return "", nil
}
func (p *privateBrowseBotTools) BindSurfBrowseContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	p.bound++
	bounded, stop := context.WithTimeout(ctx, time.Minute)
	p.ctx = bounded
	return bounded, stop, nil
}
func (p *privateBrowseBotTools) PrivateSurfAuthorization() string {
	if p.ctx != nil && p.ctx.Err() != nil {
		return ""
	}
	return "synthetic-read-only-origin"
}
func (p *privateBrowseBotTools) MemoryResultAuthorization() string {
	return p.PrivateSurfAuthorization()
}
func (p *privateBrowseBotTools) MemoryResultAuthorized(s string) bool {
	return s != "" && s == p.PrivateSurfAuthorization()
}
func (p *privateBrowseBotTools) ReviewSurf(ctx context.Context, d companion.Decision) (companion.Decision, error) {
	p.reviewed++
	return companion.Decision{Action: "silent", Reason: "surf_browse_only"}, nil
}

func TestPrivateSurfTickUsesOnlyClaimFactoryAndNeverDelivers(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprint(async), func(t *testing.T) {
			modelCalls := 0
			w, c, n := backgroundWorker(t, generationModelFunc(func(context.Context, string, []map[string]string) (string, error) {
				modelCalls++
				return `{"action":"silent","reason":"surf_not_interested"}`, nil
			}))
			c.Config.Permissions = companion.Permissions{}
			c.Tools = &privateBrowseBotTools{t: t, trap: true}
			private := &privateBrowseBotTools{t: t}
			claims := 0
			w.config.SurfTask = func(time.Time) (*SurfTask, error) {
				claims++
				return &SurfTask{Topic: "合成公开内容", PreferredKind: "text", PrivateBrowseTools: func(ctx context.Context, trace string) (companion.ToolAccess, error) {
					private.trace = trace
					return private, nil
				}}, nil
			}
			if async {
				w.enableGenerations()
				defer w.closeGenerations()
			}
			if !w.tickSurf(t.Context()) {
				t.Fatal("private task not started")
			}
			if async {
				pumpGeneration(t, w, func() bool { return len(w.generation.jobs) == 0 })
			}
			if claims != 1 || private.bound != 1 || private.listed == 0 || private.reviewed == 0 || modelCalls == 0 || len(n.submissions) != 0 {
				t.Fatal("private browsing lifecycle or no-send boundary failed", claims, private.bound, private.listed, private.reviewed, modelCalls, len(n.submissions))
			}
			if private.ctx == nil || private.ctx.Err() == nil {
				t.Fatal("completed observer origin context remains live")
			}
			if c.Config.Permissions.ReplyText || c.Config.Permissions.Proactive {
				t.Fatal("observer modified real send permissions")
			}
		})
	}
}
