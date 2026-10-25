package main

import (
	"io"
	"strings"
	"testing"
)

func TestPollWaitBoundBeforeOpeningCheckpoint(t *testing.T) {
	for _, duration := range []string{"-1s", "11m"} {
		err := pollCommand([]string{"-state", "does-not-exist", "-wait", duration}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "between 0 and 10m") {
			t.Fatalf("duration %s: %v", duration, err)
		}
	}
}
