package pluginhost

import "testing"

func TestParseUISchemaRejectsExecutableOrUnknownDeclarations(t *testing.T) {
	good := []byte(`{"version":1,"sections":[{"id":"tasks","title":"任务","component":"tasks"}]}`)
	if _, err := ParseUISchema(good); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"sections":[{"id":"tasks","title":"任务","component":"tasks","url":"https://x"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"tasks","title":"任务","component":"script"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"../tasks","title":"任务","component":"tasks"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"tasks","title":"任务","component":"tasks","binding":"window.secret"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"tasks","title":"任务","component":"tasks","action":"fetch_url"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"config","title":"配置","component":"configuration","action":"tasks.stop"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"config","title":"配置","component":"configuration","binding":"host_data.resources"}]}`),
		[]byte(`{"version":1,"sections":[{"id":"a","title":"任务一","component":"tasks"},{"id":"b","title":"任务二","component":"tasks"}]}`),
	} {
		if _, err := ParseUISchema(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
