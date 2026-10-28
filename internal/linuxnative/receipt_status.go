package linuxnative

// WithVoiceReceipts folds a complete set of actual per-note receipts into the
// job result. It does not create receipts, query the network, or resend anything.
func (s ToolStatus) WithVoiceReceipts() ToolStatus {
	if len(s.VoiceChunks) == 0 {
		return s
	}
	seen := map[int]bool{}
	for _, p := range s.VoiceChunks {
		if p.State != "accepted" || !p.HasReceipt || p.ServerID == "" || seen[p.Index] {
			return s
		}
		seen[p.Index] = true
	}
	s.State = "accepted"
	if s.ServerID == "" {
		s.ServerID = s.VoiceChunks[len(s.VoiceChunks)-1].ServerID
	}
	return s
}
