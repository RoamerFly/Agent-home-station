package admin

import (
	"context"
	"sync"
	"time"
	"wechat-bot/internal/identity"
)

// Decorate API views rather than changing persisted IDs or old audit evidence.
func (m *Manager) displayLogs(ctx context.Context) []LogEntry {
	entries := m.Logs()
	type key struct{ thread, sender string }
	keys := map[key]bool{}
	for _, entry := range entries {
		if entry.ThreadID != "" {
			keys[key{thread: entry.ThreadID}] = true
			if entry.Sender != "" {
				keys[key{entry.ThreadID, entry.Sender}] = true
			}
		}
	}
	names := map[key]string{}
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	queue := make(chan key)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range queue {
				name := ""
				if item.sender == "" {
					name = m.memoryLabel(ctx, item.thread)
				} else if resolver, ok := m.native.(interface {
					ResolveSpeaker(context.Context, string, string) (identity.Name, error)
				}); ok {
					if n, err := resolver.ResolveSpeaker(ctx, item.thread, item.sender); err == nil && identity.Valid(n) {
						name = n.Text
					}
				}
				if name != "" {
					mu.Lock()
					names[item] = name
					mu.Unlock()
				}
			}
		}()
	}
	for item := range keys {
		if ctx.Err() != nil {
			break
		}
		select {
		case queue <- item:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	for i := range entries {
		entry := &entries[i]
		if n := names[key{thread: entry.ThreadID}]; n != "" {
			entry.Source = n
		}
		entry.SenderName = names[key{entry.ThreadID, entry.Sender}]
	}
	return entries
}
