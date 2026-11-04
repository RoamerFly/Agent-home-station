package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"wechat-bot/internal/bot"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

// This capability exists only inside a claim returned by the host scheduler.
// No Input, task file, model argument or plugin result can reconstruct it.
type privateSurfOrigin struct {
	manager                                             *Manager
	account, claim, configuration, trace, prompt, topic string
	revision                                            int
	expires                                             time.Time
	parent                                              context.Context
	mu                                                  sync.Mutex
	execution                                           context.Context
	bound                                               atomic.Bool
	revoked                                             atomic.Bool
}

func (m *Manager) privateSurfConfigurationLocked() string {
	c := m.config.Companion
	b, _ := json.Marshal([]any{m.surf.SettingsRevision, m.surf.Settings, c.Enabled, c.QuietStart, c.QuietEnd, c.Timezone, c.ConversationConfig().TaskTimeoutSeconds, m.mcp.Enabled, m.mcp.WebSearchEnabled, m.mcp.WebFetchEnabled, m.mcp.MaxToolCalls})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Called with m.mu held, after plugin/runtime readiness and before the claim
// ledger is committed. The closure is neither persisted nor replayed on boot.
func (m *Manager) privateSurfFactoryLocked(now time.Time, task *bot.SurfTask) (func(context.Context, string) (companion.ToolAccess, error), error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	duration := 5 * time.Minute // previous observer default; explicit task time wins.
	if seconds := m.config.Companion.ConversationConfig().TaskTimeoutSeconds; seconds > 0 {
		duration = time.Duration(seconds) * time.Second
	}
	expires := now.Add(duration)
	if !task.QuietDeadline.IsZero() && task.QuietDeadline.Before(expires) {
		expires = task.QuietDeadline
	}
	account := m.native.Account()
	if account == "" {
		return nil, errors.New("surf_account_unavailable")
	}
	origin := &privateSurfOrigin{manager: m, account: account, claim: hex.EncodeToString(nonce[:]), configuration: m.privateSurfConfigurationLocked(), revision: m.surf.SettingsRevision, expires: expires, prompt: task.Prompt(), topic: task.Topic}
	preferred := task.PreferredKind
	var issued atomic.Bool
	return func(ctx context.Context, trace string) (companion.ToolAccess, error) {
		if trace == "" || !issued.CompareAndSwap(false, true) {
			return nil, errors.New("surf_origin_already_bound_or_invalid")
		}
		origin.trace, origin.parent = trace, ctx
		p, _, err := m.toolAccess()
		if err != nil {
			return nil, err
		}
		p.privateSurfOrigin = origin
		p.SetTrace(companion.Input{Channel: "host", Account: account, Thread: "host:surf:" + account, TraceID: trace, Text: origin.prompt, Mode: "direct", Surf: true, Preview: true, SurfTopic: origin.topic, SurfPreferredKind: preferred})
		if err := origin.check(ctx, p.trace); err != nil {
			return nil, err
		}
		return &privateSurfTools{tools: p}, nil
	}, nil
}

func (o *privateSurfOrigin) check(ctx context.Context, in companion.Input) error {
	if o == nil {
		return errors.New("surf_origin_required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	execution := o.execution
	o.mu.Unlock()
	if o.revoked.Load() || o.parent == nil || o.parent.Err() != nil || execution != nil && execution.Err() != nil || !time.Now().Before(o.expires) {
		o.revoked.Store(true)
		return errors.New("surf_origin_expired_or_cancelled")
	}
	if in.Account != o.account || in.Channel != "host" || in.Thread != "host:surf:"+o.account || in.TraceID != o.trace || in.Text != o.prompt || in.SurfTopic != o.topic || in.Sender != "" || !in.Preview || !in.Surf || in.ReportOnly || in.Mode != "direct" || in.IsGroup || in.Directed || in.MustReply || in.SurfForceShare || in.TaskID != "" || in.Source != nil || in.QuotedSource != nil || in.ReplyContext != nil || in.OriginalText != nil || len(in.UnreadMessages) != 0 || len(in.IncomingMedia) != 0 || in.ConversationDelivery || in.BackgroundResult || in.SourceOnlyContext {
		return errors.New("surf_origin_scope_changed")
	}
	m := o.manager
	if m.native.Account() != o.account || m.draining() {
		o.revoked.Store(true)
		return errors.New("surf_origin_authorization_revoked")
	}
	m.mu.Lock()
	valid := m.surf.Settings.Enabled && m.config.Companion.Enabled && !m.config.Companion.Quiet(time.Now()) && m.surf.SettingsRevision == o.revision && m.privateSurfConfigurationLocked() == o.configuration
	m.mu.Unlock()
	if !valid {
		o.revoked.Store(true)
		return errors.New("surf_origin_configuration_changed")
	}
	return nil
}

func privateSurfTool(name string) bool {
	return name == "web_search" || name == "web_fetch" || name == "web_discover"
}

func (p *permittedTools) privateSurfAuthorization() string {
	if p.privateSurfOrigin == nil || p.privateSurfOrigin.check(context.Background(), p.trace) != nil {
		return ""
	}
	o := p.privateSurfOrigin
	b, _ := json.Marshal([]any{o.account, o.claim, o.trace, o.configuration, o.revision, o.expires})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Deliberately not embedded: ordinary media, host, memory and sending optional
// interfaces on permittedTools are absent from this readonly adapter.
type privateSurfTools struct{ tools *permittedTools }

func (t *privateSurfTools) SetTrace(in companion.Input) { t.tools.SetTrace(in) }
func (t *privateSurfTools) ForkTools() companion.ToolAccess {
	return &privateSurfTools{tools: t.tools.ForkTools().(*permittedTools)}
}
func (t *privateSurfTools) List(ctx context.Context) ([]mcptools.Tool, error) {
	if err := t.tools.privateSurfOrigin.check(ctx, t.tools.trace); err != nil {
		return nil, err
	}
	return t.tools.List(ctx)
}
func (t *privateSurfTools) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return t.tools.Call(ctx, name, args)
}
func (t *privateSurfTools) ToolTimeout(string) time.Duration { return 0 }
func (t *privateSurfTools) ReviewSurf(ctx context.Context, d companion.Decision) (companion.Decision, error) {
	return t.tools.ReviewSurf(ctx, d)
}
func (t *privateSurfTools) PrivateSurfAuthorization() string {
	return t.tools.privateSurfAuthorization()
}
func (t *privateSurfTools) MemoryResultAuthorization() string { return t.PrivateSurfAuthorization() }
func (t *privateSurfTools) MemoryResultAuthorized(stamp string) bool {
	return stamp != "" && stamp == t.PrivateSurfAuthorization()
}
func (t *privateSurfTools) BindSurfBrowseContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	o := t.tools.privateSurfOrigin
	if err := o.check(ctx, t.tools.trace); err != nil {
		return nil, nil, err
	}
	if !o.bound.CompareAndSwap(false, true) {
		return nil, nil, errors.New("surf_execution_already_bound")
	}
	bounded, stop := context.WithDeadline(ctx, o.expires)
	o.mu.Lock()
	o.execution = bounded
	o.mu.Unlock()
	return bounded, stop, nil
}
