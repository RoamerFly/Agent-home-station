package admin

import (
	"sync"
	"testing"
	"wechat-bot/internal/companion"
)

func TestConcurrentToolForksKeepTraceAndMediaHandlesIsolated(t *testing.T) {
	f := setup(t)
	base, _, err := f.manager.toolAccess()
	if err != nil {
		t.Fatal(err)
	}
	a := base.ForkTools().(*permittedTools)
	b := base.ForkTools().(*permittedTools)
	var wg sync.WaitGroup
	for _, entry := range []struct {
		p              *permittedTools
		thread, source string
	}{{a, "one", "source-one"}, {b, "two", "source-two"}} {
		wg.Add(1)
		go func(p *permittedTools, thread, source string) {
			defer wg.Done()
			p.SetTrace(companion.Input{Thread: thread, TraceID: "trace-" + thread})
			p.ResetMedia()
			p.mediaMu.Lock()
			p.media["same-handle"] = companion.GeneratedMedia{ID: "same-handle", Kind: "file", SourceID: source}
			p.mediaOrigin["same-handle"] = "https://example.org/" + thread
			p.mediaMu.Unlock()
		}(entry.p, entry.thread, entry.source)
	}
	wg.Wait()
	aa, _ := a.Media("same-handle")
	bb, _ := b.Media("same-handle")
	if aa.SourceID != "source-one" || bb.SourceID != "source-two" || a.trace.Thread != "one" || b.trace.Thread != "two" {
		t.Fatal("conversation handles or trace leaked")
	}
	a.ResetMedia()
	if _, ok := b.Media("same-handle"); !ok {
		t.Fatal("reset erased another conversation")
	}
	if _, ok := base.Media("same-handle"); ok || base.trace.TraceID != "" {
		t.Fatal("job mutated the engine's shared tool access")
	}
}
