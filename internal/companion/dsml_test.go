package companion

import (
	"encoding/json"
	"testing"
)

func TestNativeToolMarkupNeverBecomesOrdinaryReply(t *testing.T) {
	raw := `<｜｜DSML｜｜ calls><｜｜DSML｜｜ invoke name="web_media_download"><｜｜DSML｜｜ parameter name="arguments" string="false">{"url":"https://example.org/a.mp3","kind":"voice"}</｜｜DSML｜｜ parameter></｜｜DSML｜｜ invoke></｜｜DSML｜｜ calls>`
	var v struct {
		Action, Tool string
		Arguments    json.RawMessage
	}
	if json.Unmarshal([]byte(normalizeToolMarkup(raw)), &v) != nil || v.Action != "tool" || v.Tool != "web_media_download" || !json.Valid(v.Arguments) {
		t.Fatal("native tool markup not interpreted")
	}
}
