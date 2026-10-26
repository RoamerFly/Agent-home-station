package agenthost

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProcessHistoryConcurrencyAndDurationHaveNoQuota(t *testing.T) {
	root := t.TempDir()
	j, e := OpenJobs(filepath.Join(root, "jobs"))
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	for i := 0; i < 205; i++ {
		p := &Process{ID: fmt.Sprintf("historical-%d", i), State: "completed"}
		j.items[p.ID] = p
		if e := j.save(p); e != nil {
			t.Fatal(e)
		}
	}
	reloaded, e := OpenJobs(filepath.Join(root, "jobs"))
	if e != nil || len(reloaded.items) != 205 {
		t.Fatal("history became a quota", e)
	}
	reloaded.Close()
	for i := 0; i < 5; i++ {
		j.items[fmt.Sprint("running-", i)] = &Process{State: "running"}
	}
	c := Defaults()
	c.Mode = "full"
	c.Roots = []string{root}
	cmd := "printf ok"
	if runtime.GOOS == "windows" {
		cmd = "Write-Output ok"
	}
	for i, seconds := range []int{0, 7201} {
		if _, e := j.Start(c, "scope", fmt.Sprint("new-", i), root, cmd, seconds, nil); e != nil {
			t.Fatal("process quota blocked work", seconds, e)
		}
	}
}
