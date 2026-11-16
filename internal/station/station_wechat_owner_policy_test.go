package station

import (
	"context"
	"testing"
	"time"

	"wechat-bot/internal/stationservices"
)

func TestStationWechatAddsOnlyBoundOwnerPrivateReplyGrant(t *testing.T) {
	dir := t.TempDir()
	host := &stationWechat{ctx: context.Background(), workers: map[string]*stationWechatWorker{}, ownerReader: func() (stationservices.Owner, error) {
		return stationservices.Owner{FishID: "fish", ID: "local-owner"}, nil
	}}
	worker := &stationWechatWorker{connectionID: "wechat-one", host: host, dir: dir, runtimeDir: dir}
	host.workers[worker.connectionID] = worker
	boundAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	if err := worker.saveWechatOwner(stationWechatOwnerState{Account: "one", Revision: 1, Binding: &stationWechatOwnerBinding{Sender: "wx-owner", Dataset: "dataset", Message: "msg", Fish: "fish", Owner: "local-owner", BoundAt: boundAt}}); err != nil {
		t.Fatal(err)
	}
	policy := stationWechatPolicy{Grants: map[string]time.Time{"room@chatroom": boundAt}}
	result := host.addBoundOwnerReplyGrant(worker.connectionID, "one", policy)
	if got := result.Grants["wx-owner"]; !got.Equal(boundAt) {
		t.Fatalf("owner private thread was not granted from binding: %v", got)
	}
	if _, ok := result.Grants["other-private"]; ok {
		t.Fatal("unbound private conversation entered reply grants")
	}
}
