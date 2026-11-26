package admin

import (
	"strings"
	"testing"
)

func TestPublicBiliViewAndDASHProofFromSamePlayback(t *testing.T) {
	page := "https://www.bilibili.com/video/BV1qP4y187Lf/?p=2"
	view := publicBiliViewURL(page)
	if view != "https://api.bilibili.com/x/web-interface/view?bvid=BV1qP4y187Lf" {
		t.Fatal(view)
	}
	p := &publicSharePolicy{pages: map[string]string{page: "text/html"}, allowedReads: map[string]bool{}, locators: map[string]bool{}}
	p.allowBiliViewPlayback(view, []byte(`{"code":0,"data":{"bvid":"BV1qP4y187Lf","cid":11,"pages":[{"cid":11},{"cid":22}]}}`))
	playbacks := publicBiliPlaybackURLs("BV1qP4y187Lf", 22)
	if len(playbacks) != 2 || !p.allowedReads[playbacks[0]] || !p.allowedReads[playbacks[1]] || p.allowedReads[strings.Replace(playbacks[0], "cid=22", "cid=11", 1)] {
		t.Fatal("view granted wrong playback URL", p.allowedReads)
	}
	video, audio := "https://a.edge.mountaintoys.cn/video.mp4?key=1", "https://b.edge.mountaintoys.cn/audio.m4a?key=2"
	body := []byte(`{"code":0,"data":{"dash":{"video":[{"baseUrl":"` + video + `"}],"audio":[{"baseUrl":"` + audio + `"}]}}}`)
	for locator := range publicShareLocations(body, playbacks[1]) {
		p.locators[locator] = true
	}
	p.recordBiliStreamPairs(playbacks[1], body)
	if !p.streamPairs[publicShareStreamPair{VideoURL: video, AudioURL: audio}] {
		t.Fatal("same-response DASH tracks were not recorded")
	}
	foreign := strings.Replace(playbacks[1], "cid=22", "cid=23", 1)
	p.recordBiliStreamPairs(foreign, []byte(`{"code":0,"data":{"dash":{"video":[{"baseUrl":"https://x.example/v.mp4"}],"audio":[{"baseUrl":"https://y.example/a.m4a"}]}}}`))
	if len(p.streamPairs) != 1 {
		t.Fatal("unverified playback added tracks", p.streamPairs)
	}
}
