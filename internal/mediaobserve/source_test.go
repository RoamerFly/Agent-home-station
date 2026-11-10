package mediaobserve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"wechat-bot/internal/pluginhost"
)

func TestSourceCheckRevalidatesOriginAndTool(t *testing.T) {
	ctx := t.Context()
	store, err := pluginhost.OpenStore(t.TempDir(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	prepared, err := store.Prepare(ctx, filepath.Join("..", "..", "plugins", "media-understanding"))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.Install(ctx, prepared.Token, prepared.Digest, []string{"artifact.read", "artifact.write", "model.invoke"})
	if err != nil {
		t.Fatal(err)
	}
	id := installed.Package.ID
	installed, err = store.SetEnabled(ctx, id, installed.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Configuration(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	var originErr error
	check := BindSource(store, installed, config.Revision, func(context.Context) error { return originErr })
	if err := check.CheckJob(ctx, pluginhost.Job{}, pluginhost.Capability{}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.SaveIdleConfiguration(ctx, id, pluginhost.ConfigUpdate{
		Revision: config.Revision, Values: map[string]json.RawMessage{"vision_model": json.RawMessage(`"synthetic-updated"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); err == nil || err.Error() != "media_plugin_configuration_changed" {
		t.Fatalf("changed configuration returned %v", err)
	}
	originErr = errors.New("synthetic source revoked")
	if err := check(ctx); !errors.Is(err, originErr) {
		t.Fatalf("revoked source returned %v", err)
	}
	originErr = nil

	check = BindSource(store, installed, updated.Revision, func(context.Context) error { return nil })
	if _, err := store.SetEnabled(ctx, id, installed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if err := check(ctx); err == nil || err.Error() != "media_plugin_version_changed" {
		t.Fatalf("disabled tool returned %v", err)
	}
}

func TestCopyVerifiedSource(t *testing.T) {
	const body = "synthetic bytes"
	sha := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	for _, tc := range []struct {
		name, input, copied string
		want                error
	}{
		{name: "exact", input: body, copied: body},
		{name: "short", input: body[:4], copied: body[:4], want: ErrSourceIntegrity},
		{name: "long", input: body + "!", copied: body, want: ErrSourceIntegrity},
		{name: "changed", input: body[:len(body)-1] + "!", copied: body[:len(body)-1] + "!", want: ErrSourceIntegrity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := CopyVerifiedSource(t.Context(), &out, strings.NewReader(tc.input), int64(len(body)), sha, func() error { return nil })
			if !errors.Is(err, tc.want) || out.String() != tc.copied {
				t.Fatalf("copied %q, error %v", out.String(), err)
			}
		})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	err := CopyVerifiedSource(ctx, &out, strings.NewReader(body), int64(len(body)), sha, func() error { return nil })
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled copy wrote %d bytes, error %v", out.Len(), err)
	}
}

type sourceCopyDestination struct {
	written int
}

func (w *sourceCopyDestination) Write(p []byte) (int, error) {
	w.written += len(p)
	return len(p), nil
}

func TestCopyVerifiedSourceStopsAfterRevocation(t *testing.T) {
	body := strings.Repeat("a", 128<<10)
	sha := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	out := &sourceCopyDestination{}
	denied := errors.New("synthetic source revoked")
	err := CopyVerifiedSource(t.Context(), out, strings.NewReader(body), int64(len(body)), sha, func() error {
		if out.written != 0 {
			return denied
		}
		return nil
	})
	if !errors.Is(err, denied) || out.written == 0 || out.written >= len(body) {
		t.Fatalf("revoked copy wrote %d of %d bytes, error %v", out.written, len(body), err)
	}
}
