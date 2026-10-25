package admin

import "wechat-bot/internal/companion"

func (p *permittedTools) musicKindPermitted(kind string) bool {
	perms := p.manager.Config().Companion.Permissions
	if kind == "both" {
		return perms.SendVoice && perms.SendFile
	}
	return companion.MediaPermitted(perms, kind)
}

// The model owns request semantics. Do not reinterpret its explicit format with
// phrase matching. An omitted format inherits the existing task or defaults.
func (p *permittedTools) musicDeliveryKind(requested string) string {
	if requested != "" {
		return requested
	}
	if p.trace.TaskID != "" {
		for _, task := range p.manager.agent.list(p.trace.Account, p.trace.Thread) {
			if task.ID == p.trace.TaskID && task.MusicKind != "" {
				return task.MusicKind
			}
		}
	}
	if !p.manager.Config().Companion.Permissions.SendVoice {
		return "file"
	}
	return "voice"
}
