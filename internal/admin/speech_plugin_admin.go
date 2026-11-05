package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"wechat-bot/internal/pluginhost"
)

type speechAdminTestContextKey struct{}

// This authority belongs to the authenticated local management request. It
// carries no WeChat sender, conversation source or permission to send media.
type speechAdminTestAuthority struct {
	Account   string
	Session   string // irreversible session fingerprint, never a cookie
	RequestID string
	Check     func(context.Context) error
}

func (a *speechAdminTestAuthority) valid(ctx context.Context, m *Manager) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || m == nil || a.Account == "" || a.Account != m.native.Account() || a.Session == "" || a.RequestID == "" || a.Check == nil || m.draining() {
		return errors.New("speech_admin_test_not_authorized")
	}
	m.packageMu.Lock()
	closed := m.packagesClosed
	m.packageMu.Unlock()
	if closed {
		return errors.New("speech_admin_test_not_authorized")
	}
	return a.Check(ctx)
}

func (s *Server) speechPluginTestContext(r *http.Request) context.Context {
	id, _, ok := s.session(r)
	if !ok {
		return r.Context()
	}
	hash := sha256.Sum256([]byte(id))
	account := s.manager.native.Account()
	authority := &speechAdminTestAuthority{Account: account, Session: hex.EncodeToString(hash[:]), RequestID: randomToken()}
	authority.Check = func(ctx context.Context) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, _, valid := s.session(r)
		if !valid || current != id || s.manager.native.Account() != account {
			return errors.New("speech_admin_session_changed")
		}
		return nil
	}
	return context.WithValue(r.Context(), speechAdminTestContextKey{}, authority)
}

func (m *Manager) newAdminSpeechPluginPolicy(ctx context.Context, text string) (*speechPluginPolicy, error) {
	authority, _ := ctx.Value(speechAdminTestContextKey{}).(*speechAdminTestAuthority)
	if err := authority.valid(ctx, m); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]any{"text": text, "timeout_seconds": 150})
	policy, err := newSpeechPluginRequest(ctx, m, raw)
	if err != nil {
		return nil, err
	}
	policy.admin = authority
	auth := agentID(authority.Account, authority.Session, authority.RequestID, policy.packageDigest, policy.packageVersion, strconv.Itoa(policy.configRevision), policy.model, policy.voice)
	policy.invocation = pluginhost.Invocation{Account: authority.Account, Thread: "host:admin-speech-test", Requester: "administrator", Purpose: "admin speech synthesis test", RequestKey: "admin-speech-test-" + agentID(auth, text, policy.instruction), Authorization: auth}
	return policy, nil
}

// The management route receives only a verified provider result; it never
// enters chat staging, the delivery ledger or any native WeChat send method.
func (m *Manager) synthesizePluginSpeechTest(ctx context.Context, text string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	policy, err := m.newAdminSpeechPluginPolicy(bounded, text)
	if err != nil {
		return "", err
	}
	result, err := policy.synthesize(bounded)
	if err != nil {
		return "", err
	}
	if err := policy.checkCompleted(bounded); err != nil {
		return "", err
	}
	return result.URL, nil
}
