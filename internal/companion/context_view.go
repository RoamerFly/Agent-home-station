package companion

import "errors"

var ErrCompressionInProgress = errors.New("context_compression_in_progress")

// FoldedHistoryCount is a presentation boundary, not the model's context
// boundary. The model still reads the uncompressed recent tail after Summary.
func (s Scope) FoldedHistoryCount() int {
	if s.Summary == "" {
		return 0
	}
	boundary := s.SummaryBoundary
	if boundary == 0 && !s.SummaryUpdated.IsZero() {
		// Compatibility with summaries written before snapshot lengths existed.
		for i, message := range s.Messages {
			if !message.Time.After(s.SummaryUpdated) {
				boundary = i + 1
			}
		}
	}
	return min(len(s.Messages), max(boundary, s.CompressedCount))
}

func (s *Store) beginCompression(id string) (func(), bool) {
	s.compressionMu.Lock()
	defer s.compressionMu.Unlock()
	if s.compressing[id] {
		return nil, false
	}
	if s.compressing == nil {
		s.compressing = map[string]bool{}
	}
	s.compressing[id] = true
	return func() {
		s.compressionMu.Lock()
		delete(s.compressing, id)
		s.compressionMu.Unlock()
	}, true
}
