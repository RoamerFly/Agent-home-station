package admin

import (
	"context"
	"errors"
	"time"
	"wechat-bot/internal/companion"
	"wechat-bot/internal/identity"
)

func (m *Manager) memoryLabel(ctx context.Context, thread string) string {
	if thread == "filehelper" {
		return "文件传输助手"
	}
	label := thread
	for _, rule := range m.Config().GroupRules {
		if rule.ID == thread && rule.Name != "" {
			label = rule.Name
			break
		}
	}
	if resolver, ok := m.native.(interface {
		ResolveConversation(context.Context, string) (identity.Name, error)
	}); ok && ctx.Err() == nil {
		if name, err := resolver.ResolveConversation(ctx, thread); err == nil && identity.Valid(name) {
			label = name.Text
		}
	}
	return label
}

func (m *Manager) namedMemory(ctx context.Context, scope companion.Scope) (companion.Scope, error) {
	if scope.Channel != "wechat" || scope.Account != m.native.Account() {
		return companion.Scope{}, errors.New("memory identity mismatch")
	}
	c := m.Config().Companion
	nameCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	lookup, _ := m.native.(companion.NameLookup)
	names := companion.ResolveNames(nameCtx, lookup, scope.Thread, companion.ScopeSenders(scope))
	if err := m.memory.UpdateNames(companion.Input{Channel: scope.Channel, Account: scope.Account, Thread: scope.Thread}, c, names); err != nil {
		return companion.Scope{}, err
	}
	return companion.NamedScope(scope, names, c.Name), nil
}
