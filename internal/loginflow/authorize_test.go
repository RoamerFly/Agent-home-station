package loginflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"wechat-bot/internal/mmtls"
)

type failedAuthorizationCarrier struct{ calls int }

func (c *failedAuthorizationCarrier) Handshake(context.Context) (mmtls.Session, error) {
	return mmtls.Session{}, errors.New("unexpected handshake")
}
func (c *failedAuthorizationCarrier) Exchange(context.Context, mmtls.Session, string, []byte) ([]byte, error) {
	c.calls++
	return nil, errors.New("synthetic connection lost after request")
}

func syntheticConfirmedPending() Pending {
	confirmed := uint32(2)
	return Pending{Profile: Profile, Host: "short.weixin.qq.com", DeviceID: append([]byte{0x49}, make([]byte, 15)...), RandomKey: make([]byte, 16), NotifyKey: make([]byte, 16), UUID: "synthetic-uuid", ExpiresAt: time.Now().Unix() + 60, MMTLS: mmtls.Session{PSK: make([]byte, 32), Ticket: make([]byte, 5)}, LastStatus: &confirmed, UserName: "synthetic-user", TemporaryPassword: "synthetic-password"}
}

func TestUnknownAuthorizationOutcomeIsNotResubmitted(t *testing.T) {
	p := syntheticConfirmedPending()
	c := &failedAuthorizationCarrier{}
	_, err := Authorize(context.Background(), c, &p, AuthorizationOptions{})
	if err == nil || !strings.Contains(err.Error(), "outcome unknown") || p.AuthorizationAttemptedAt == 0 || c.calls != 1 {
		t.Fatalf("first attempt state incorrect: err=%v calls=%d marked=%v", err, c.calls, p.AuthorizationAttemptedAt != 0)
	}
	_, err = Authorize(context.Background(), c, &p, AuthorizationOptions{})
	if err == nil || !strings.Contains(err.Error(), "already attempted") || c.calls != 1 {
		t.Fatalf("attempt was resubmitted: err=%v calls=%d", err, c.calls)
	}
}

func TestFailedDurableMarkerPreventsRequest(t *testing.T) {
	p := syntheticConfirmedPending()
	c := &failedAuthorizationCarrier{}
	called := false
	_, err := Authorize(context.Background(), c, &p, AuthorizationOptions{Checkpoint: func(state *Pending) error {
		called = true
		if state.AuthorizationAttemptedAt == 0 {
			t.Fatal("marker was not set before persistence")
		}
		return errors.New("synthetic disk full")
	}})
	if err == nil || !called || c.calls != 0 || !strings.Contains(err.Error(), "request not sent") {
		t.Fatalf("persistence failure allowed exchange: err=%v callback=%v calls=%d", err, called, c.calls)
	}
}

func TestSpamRejectionNamesRecoveredEnum(t *testing.T) {
	err := (&AuthorizationRejected{Code: -106}).Error()
	if !strings.Contains(err, "MM_ERR_BLOCK_BY_SPAM") || !strings.Contains(err, "no account session") {
		t.Fatal(err)
	}
}

func TestUpgradeInstructionMustComeFromServerContent(t *testing.T) {
	message := "<e><Content><![CDATA[你的应用版本过低，请升级至最新版本后再登录。]]></Content><Url>https://example.invalid/private</Url></e>"
	if !serverRequiresUpgrade(&message) {
		t.Fatal("explicit server upgrade instruction ignored")
	}
	err := (&AuthorizationRejected{Code: -106, UpgradeRequired: true}).Error()
	if !strings.Contains(err, "newer application version") || strings.Contains(err, "example.invalid") {
		t.Fatal("wrong safe upgrade error")
	}
	message = "<e><Content>其他拒绝原因</Content><Url>应用版本过低 升级</Url></e>"
	if serverRequiresUpgrade(&message) || serverRequiresUpgrade(nil) {
		t.Fatal("upgrade inferred outside actual content")
	}
}
