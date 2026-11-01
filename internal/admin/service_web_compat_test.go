package admin

import "testing"

func TestWebServiceCapabilityMapsAudioAliasToInstalledSearch(t *testing.T) {
	if got := webServiceCapability("web_audio_search"); got != "web.search" {
		t.Fatalf("audio alias capability=%q", got)
	}
	if got := webServiceCapability("web_fetch"); got != "web.read" {
		t.Fatalf("fetch capability=%q", got)
	}
}
