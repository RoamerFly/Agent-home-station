package admin

// Tools still enforce their own identity and target permissions. This flag
// removes an arbitrary turn budget, not those authorization checks.
func (p *permittedTools) AutonomousActions() bool {
	return p.manager.agent != nil && p.manager.agent.config().Enabled && !p.trace.Preview && !p.trace.ReportOnly && !p.trace.Surf && permittedRecipient(p.manager.Config().Config, p.trace.Thread)
}
