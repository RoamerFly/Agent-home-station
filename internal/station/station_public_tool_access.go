package station

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"wechat-bot/internal/companion"
	"wechat-bot/internal/mcptools"
)

// publicToolAccess joins the application-owned WeChat tools with the
// workstation-owned public tools. The two surfaces remain separate: the
// application decides which messages and attachments may be read, while the
// workstation decides which registered generators and project tools the
// owner has granted.
//
// The public access object is injected by the Station host. It is not a second
// runtime and never copies a plugin, model or credential into the application
// directory.
type publicToolAccess struct {
	primary companion.ToolAccess
	public  companion.ToolAccess
	allow   func(context.Context) bool

	mu          sync.RWMutex
	primaryName map[string]struct{}
	publicName  map[string]struct{}
}

// publicToolFactoryAccess delays construction of the workstation tool set
// until the WeChat runner has supplied the current message identity. This
// keeps local-memory and process checkpoints in the workstation scope while
// still giving each forked WeChat conversation an isolated tool object.
type publicToolFactoryAccess struct {
	factory func(companion.Input) companion.ToolAccess
	allow   func(companion.Input) bool
	trace   companion.Input
	current companion.ToolAccess
}

func newPublicToolFactoryAccess(factory func(companion.Input) companion.ToolAccess, allow func(companion.Input) bool) companion.ToolAccess {
	if factory == nil {
		return nil
	}
	return &publicToolFactoryAccess{factory: factory, allow: allow}
}

func (a *publicToolFactoryAccess) SetTrace(in companion.Input) {
	a.trace, a.current = in, nil
}

func (a *publicToolFactoryAccess) ForkTools() companion.ToolAccess {
	if a == nil {
		return nil
	}
	return &publicToolFactoryAccess{factory: a.factory, allow: a.allow}
}

func (a *publicToolFactoryAccess) allowed() bool {
	return a != nil && a.factory != nil && (a.allow == nil || a.allow(a.trace))
}

func (a *publicToolFactoryAccess) access() companion.ToolAccess {
	if !a.allowed() {
		return nil
	}
	if a.current == nil {
		a.current = a.factory(a.trace)
	}
	return a.current
}

func (a *publicToolFactoryAccess) List(ctx context.Context) ([]mcptools.Tool, error) {
	if !a.allowed() {
		return nil, nil
	}
	access := a.access()
	if access == nil {
		return nil, nil
	}
	return access.List(ctx)
}

func (a *publicToolFactoryAccess) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if !a.allowed() {
		return "", errors.New("工作站公共工具未获当前微信发送者授权")
	}
	access := a.access()
	if access == nil {
		return "", errors.New("工作站公共工具未初始化")
	}
	return access.Call(ctx, name, raw)
}

func (a *publicToolFactoryAccess) AutonomousActions() bool {
	access := a.access()
	if autonomous, ok := access.(interface{ AutonomousActions() bool }); ok {
		return autonomous.AutonomousActions()
	}
	return false
}

func (a *publicToolFactoryAccess) ToolTimeout(name string) time.Duration {
	access := a.access()
	if timeout, ok := access.(interface{ ToolTimeout(string) time.Duration }); ok {
		return timeout.ToolTimeout(name)
	}
	return 0
}

// mergeStationToolAccess creates the per-turn tool surface. primary is
// normally the WeChat attachment/permission adapter. public may be nil when
// the Station has not granted public tools for this turn.
func mergeStationToolAccess(primary, public companion.ToolAccess, allow func(context.Context) bool) companion.ToolAccess {
	if public == nil {
		return primary
	}
	return &publicToolAccess{primary: primary, public: public, allow: allow,
		primaryName: map[string]struct{}{}, publicName: map[string]struct{}{}}
}

func (a *publicToolAccess) publicAllowed(ctx context.Context) bool {
	return a != nil && a.public != nil && (a.allow == nil || a.allow(ctx))
}

func (a *publicToolAccess) SetTrace(in companion.Input) {
	if traced, ok := a.primary.(interface{ SetTrace(companion.Input) }); ok {
		traced.SetTrace(in)
	}
	if traced, ok := a.public.(interface{ SetTrace(companion.Input) }); ok {
		traced.SetTrace(in)
	}
}

func (a *publicToolAccess) ForkTools() companion.ToolAccess {
	if a == nil {
		return nil
	}
	primary, public := a.primary, a.public
	if fork, ok := primary.(interface{ ForkTools() companion.ToolAccess }); ok {
		primary = fork.ForkTools()
	}
	if fork, ok := public.(interface{ ForkTools() companion.ToolAccess }); ok {
		public = fork.ForkTools()
	}
	return mergeStationToolAccess(primary, public, a.allow)
}

func (a *publicToolAccess) AutonomousActions() bool {
	for _, access := range []companion.ToolAccess{a.primary, a.public} {
		if autonomous, ok := access.(interface{ AutonomousActions() bool }); ok && autonomous.AutonomousActions() {
			return true
		}
	}
	return false
}

func (a *publicToolAccess) ToolTimeout(name string) time.Duration {
	a.mu.RLock()
	_, isPublic := a.publicName[name]
	a.mu.RUnlock()
	if isPublic {
		if timeout, ok := a.public.(interface{ ToolTimeout(string) time.Duration }); ok {
			return timeout.ToolTimeout(name)
		}
	}
	if timeout, ok := a.primary.(interface{ ToolTimeout(string) time.Duration }); ok {
		return timeout.ToolTimeout(name)
	}
	return 0
}

// Media exposes prepared delivery handles owned by the permission-sensitive
// application adapter. The merged access object must preserve this capability
// so the companion engine can validate media_ids after a tool prepares them.
func (a *publicToolAccess) Media(id string) (companion.GeneratedMedia, bool) {
	if a == nil || a.primary == nil {
		return companion.GeneratedMedia{}, false
	}
	access, ok := a.primary.(interface {
		Media(string) (companion.GeneratedMedia, bool)
	})
	if !ok {
		return companion.GeneratedMedia{}, false
	}
	return access.Media(id)
}

func (a *publicToolAccess) List(ctx context.Context) ([]mcptools.Tool, error) {
	if a == nil {
		return nil, errors.New("工具访问未初始化")
	}
	var (
		primaryTools []mcptools.Tool
		publicTools  []mcptools.Tool
		primaryErr   error
		publicErr    error
	)
	if a.primary != nil {
		primaryTools, primaryErr = a.primary.List(ctx)
	}
	if a.publicAllowed(ctx) {
		publicTools, publicErr = a.public.List(ctx)
	}

	primaryNames := make(map[string]struct{}, len(primaryTools))
	for _, tool := range primaryTools {
		if tool.Name != "" {
			primaryNames[tool.Name] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(primaryTools)+len(publicTools))
	merged := make([]mcptools.Tool, 0, len(primaryTools)+len(publicTools))
	for _, tool := range primaryTools {
		if tool.Name == "" {
			continue
		}
		if _, ok := seen[tool.Name]; ok {
			continue
		}
		seen[tool.Name] = struct{}{}
		merged = append(merged, tool)
	}
	publicNames := make(map[string]struct{}, len(publicTools))
	for _, tool := range publicTools {
		if tool.Name == "" {
			continue
		}
		// The application surface wins on a collision. A public package must
		// not shadow a permission-sensitive WeChat operation.
		if _, collision := primaryNames[tool.Name]; collision {
			continue
		}
		if _, ok := seen[tool.Name]; ok {
			continue
		}
		seen[tool.Name] = struct{}{}
		publicNames[tool.Name] = struct{}{}
		merged = append(merged, tool)
	}
	a.mu.Lock()
	a.primaryName, a.publicName = primaryNames, publicNames
	a.mu.Unlock()
	return merged, errors.Join(primaryErr, publicErr)
}

func (a *publicToolAccess) Call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if a == nil {
		return "", errors.New("工具访问未初始化")
	}
	a.mu.RLock()
	_, isPublic := a.publicName[name]
	_, isPrimary := a.primaryName[name]
	a.mu.RUnlock()
	if isPublic {
		if !a.publicAllowed(ctx) {
			return "", errors.New("工作站公共工具授权已撤销")
		}
		// The workstation can change an installation, grant or configuration
		// after the model saw this turn's list. Use its current directory at the
		// point of execution instead of dispatching from the cached name alone.
		current, err := a.public.List(ctx)
		if err != nil {
			return "", err
		}
		available := false
		for _, tool := range current {
			if tool.Name == name {
				available = true
				break
			}
		}
		if !available {
			return "", errors.New("工作站公共工具当前不可用")
		}
		return a.public.Call(ctx, name, raw)
	}
	// A name that was only present on the public surface must not fall through
	// to the application adapter after the grant is revoked. This avoids both
	// accidental dispatch and a confusing "unknown WeChat tool" response.
	if a.public != nil && !a.publicAllowed(ctx) && !isPrimary {
		return "", errors.New("工作站公共工具授权已撤销")
	}
	if a.primary == nil {
		return "", errors.New("应用工具未初始化")
	}
	return a.primary.Call(ctx, name, raw)
}

var _ companion.ToolAccess = (*publicToolAccess)(nil)
