package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestActivationLocksConfigurationAndScopesPrivateValues(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(t.TempDir(), "a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	i := installConfigFixture(t, s)
	if _, err = s.ConfigurationForExecution(ctx, i.Package.ID); err == nil {
		t.Fatal("disabled secret access")
	}
	c, err := s.SaveConfiguration(ctx, i.Package.ID, ConfigUpdate{Secrets: map[string]string{"key": "synthetic-local-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetEnabled(ctx, i.Package.ID, i.Revision+1, true); !errors.Is(err, ErrRevision) {
		t.Fatal("stale activation accepted", err)
	}
	i, err = s.SetEnabled(ctx, i.Package.ID, i.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.ConfigurationForExecution(ctx, i.Package.ID)
	if err != nil || private.Revision != c.Revision || string(private.Values["key"]) != `"synthetic-local-secret"` {
		t.Fatal("configuration binding lost")
	}
	if _, err = s.SaveConfiguration(ctx, i.Package.ID, ConfigUpdate{Revision: c.Revision}); err == nil {
		t.Fatal("enabled configuration edited")
	}
	if _, err = s.PrivateDirectory(ctx, "../other"); err == nil {
		t.Fatal("data path escaped")
	}
	if _, err = s.ConfigurationForExecution(ctx, "other"); err == nil {
		t.Fatal("unknown plugin secrets exposed")
	}
	public, err := s.Configuration(ctx, i.Package.ID)
	if err != nil {
		t.Fatal(err)
	}
	if public.Values["key"] != nil {
		t.Fatal("secret exposed publicly")
	}
	i, err = s.SetEnabled(ctx, i.Package.ID, i.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveConfiguration(ctx, i.Package.ID, ConfigUpdate{Revision: c.Revision, Values: map[string]json.RawMessage{"count": json.RawMessage(`7`)}}); err != nil {
		t.Fatal(err)
	}
}

func TestActivationRejectsMalformedPrivateConfiguration(t *testing.T) {
	for _, row := range []struct{ values, secrets string }{
		{`null`, `{}`}, {`{}`, `null`},
		{`{"key":"synthetic-incorrect-location"}`, `{}`},
		{`{}`, `{"foreign":"synthetic-undeclared"}`},
	} {
		s, e := OpenStore(t.TempDir(), "a")
		if e != nil {
			t.Fatal(e)
		}
		i := installConfigFixture(t, s)
		_, e = s.db.Exec(`INSERT INTO configurations(plugin,revision,values_json,secrets_json) VALUES(?,1,?,?)`, i.Package.ID, row.values, row.secrets)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.SetEnabled(context.Background(), i.Package.ID, i.Revision, true); e == nil {
			t.Error("corrupt config activated")
		}
		s.Close()
	}
}
