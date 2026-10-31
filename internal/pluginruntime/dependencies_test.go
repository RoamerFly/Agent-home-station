package pluginruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func dependencyFixture(t *testing.T, graph map[string][]string, disabled ...string) (*pluginhost.Store, map[string]pluginhost.InstalledPackage) {
	t.Helper()
	s, err := pluginhost.OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	items := map[string]pluginhost.InstalledPackage{}
	for id, deps := range graph {
		dir := t.TempDir()
		p := readyFixture(t, dir)
		p.ID, p.Dependencies = id, deps
		raw, _ := json.Marshal(p)
		if err = os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		prepared, e := s.Prepare(t.Context(), dir)
		if e != nil {
			t.Fatal(e)
		}
		i, e := s.Install(t.Context(), prepared.Token, prepared.Digest, nil)
		if e != nil {
			t.Fatal(e)
		}
		enabled := true
		for _, key := range disabled {
			if key == id {
				enabled = false
			}
		}
		if enabled {
			i, e = s.SetEnabled(t.Context(), id, i.Revision, true)
			if e != nil {
				t.Fatal(e)
			}
		}
		i, e = s.Installed(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		items[id] = i
	}
	return s, items
}

type dependencySession struct {
	invocationSession
	alive atomic.Bool
}

func (s *dependencySession) Alive() bool { return s.alive.Load() }

func TestDependencyGraphStartsTopologicallyOnceWithoutExecutingTools(t *testing.T) {
	s, items := dependencyFixture(t, map[string][]string{"leaf": nil, "left": {"leaf"}, "right": {"leaf"}, "root": {"left", "right"}}, "root")
	order := []string{}
	sessions := map[string]*dependencySession{}
	c, _ := NewController(s, startFunc(func(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
		order = append(order, p.ID)
		x := &dependencySession{}
		x.alive.Store(true)
		sessions[p.ID] = x
		return x, nil
	}))
	defer c.Close()
	if err := c.Start(t.Context(), "root", items["root"].Revision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"leaf", "left", "right", "root"}) {
		t.Fatal(order)
	}
	for _, x := range sessions {
		if x.calls != 0 {
			t.Fatal("start executed a capability")
		}
	}
	i, _ := s.Installed(t.Context(), "root")
	if err := c.Start(t.Context(), "root", i.Revision); err != nil || len(order) != 4 {
		t.Fatal("restarted running dependency", err, order)
	}
	if err := c.Stop(t.Context(), "leaf", items["leaf"].Revision); err == nil {
		t.Fatal("stopped dependency of a running plugin")
	}
	// A dead grandchild invalidates calls even while both immediate children live.
	sessions["leaf"].alive.Store(false)
	if _, err := c.host.Call(t.Context(), "root", invocation(), "echo", json.RawMessage(`{}`), nil); err == nil || sessions["root"].calls != 0 {
		t.Fatal("called plugin with crashed transitive dependency")
	}
}

func TestDependencyPlanRejectsMissingDisabledAndCyclesBeforeAnyStartup(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		graph      map[string][]string
		disabled   []string
	}{
		{"missing", "dependency_missing", map[string][]string{"root": {"leaf", "missing"}, "leaf": nil}, []string{"root"}},
		{"disabled", "dependency_disabled", map[string][]string{"root": {"leaf"}, "leaf": nil}, []string{"root", "leaf"}},
		{"cycle", "dependency_cycle", map[string][]string{"root": {"leaf"}, "leaf": {"root"}}, []string{"root"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, items := dependencyFixture(t, tc.graph, tc.disabled...)
			calls := 0
			c, _ := NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
				calls++
				return &invocationSession{}, nil
			}))
			defer c.Close()
			err := c.Start(t.Context(), "root", items["root"].Revision)
			var dep *DependencyError
			if !errors.As(err, &dep) || dep.Code != tc.code || calls != 0 {
				t.Fatal(err, calls)
			}
			for id, before := range items {
				after, _ := s.Installed(t.Context(), id)
				if !reflect.DeepEqual(after, before) {
					t.Fatal("preflight changed state", id)
				}
			}
		})
	}
}

func TestDependencyStartupFailureReportsPartialStateAndNeverRunsRoot(t *testing.T) {
	s, items := dependencyFixture(t, map[string][]string{"root": {"leaf", "broken"}, "leaf": nil, "broken": nil}, "root")
	order := []string{}
	c, _ := NewController(s, startFunc(func(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
		order = append(order, p.ID)
		if p.ID == "broken" {
			return nil, errors.New("synthetic private failure")
		}
		return &invocationSession{}, nil
	}))
	defer c.Close()
	err := c.Start(t.Context(), "root", items["root"].Revision)
	var dep *DependencyError
	if !errors.As(err, &dep) || dep.Code != "dependency_start_failed" || dep.Dependency != "broken" || !reflect.DeepEqual(dep.Started, []string{"leaf"}) || !reflect.DeepEqual(order, []string{"leaf", "broken"}) {
		t.Fatal(err, order)
	}
	i, _ := s.Installed(t.Context(), "root")
	if i.Enabled || c.Status("leaf").State != "running" {
		t.Fatal("partial startup state lost")
	}
	if c.Status("broken").State != "failed" {
		t.Fatal("failed dependency shown as stopped")
	}
}

func TestDependencyPlanChecksRootConfigurationBeforeStartingDependencies(t *testing.T) {
	s, _ := dependencyFixture(t, map[string][]string{"leaf": nil})
	dir := t.TempDir()
	p := readyFixture(t, dir)
	p.ID, p.Dependencies = "root", []string{"leaf"}
	schema := []byte(`{"type":"object","properties":{"api_key":{"type":"string","x-secret":true}},"required":["api_key"],"additionalProperties":false}`)
	sum := sha256.Sum256(schema)
	p.Files["config.schema.json"] = hex.EncodeToString(sum[:])
	raw, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.schema.json"), schema, 0600); err != nil {
		t.Fatal(err)
	}
	prepared, err := s.Prepare(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	i, err := s.Install(t.Context(), prepared.Token, prepared.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c, _ := NewController(s, startFunc(func(context.Context, pluginhost.Package, pluginhost.Grant) (pluginhost.Session, error) {
		calls++
		return &invocationSession{}, nil
	}))
	defer c.Close()
	err = c.Start(t.Context(), "root", i.Revision)
	var dep *DependencyError
	if !errors.As(err, &dep) || dep.Code != "dependency_configuration" || dep.Dependency != "root" || calls != 0 {
		t.Fatal(err, calls)
	}
	if _, err = s.SaveConfiguration(t.Context(), "root", pluginhost.ConfigUpdate{Secrets: map[string]string{"api_key": "synthetic-only"}}); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(t.Context(), "root", i.Revision); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}

func TestDependencyGraphReservationRejectsOverlappingStartAndStop(t *testing.T) {
	s, items := dependencyFixture(t, map[string][]string{"one": {"leaf"}, "two": {"leaf"}, "leaf": nil}, "one", "two")
	entered, release := make(chan struct{}), make(chan struct{})
	c, _ := NewController(s, startFunc(func(_ context.Context, p pluginhost.Package, _ pluginhost.Grant) (pluginhost.Session, error) {
		if p.ID == "leaf" {
			close(entered)
			<-release
		}
		return &invocationSession{}, nil
	}))
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- c.Start(t.Context(), "one", items["one"].Revision) }()
	<-entered
	second := c.Start(t.Context(), "two", items["two"].Revision)
	stop := c.Stop(t.Context(), "leaf", items["leaf"].Revision)
	close(release)
	first := <-done
	var dep *DependencyError
	if first != nil || !errors.As(second, &dep) || dep.Code != "dependency_busy" || stop == nil {
		t.Fatal(first, second, stop)
	}
	i, _ := s.Installed(t.Context(), "two")
	if i.Enabled {
		t.Fatal("overlapping request enabled root")
	}
}
