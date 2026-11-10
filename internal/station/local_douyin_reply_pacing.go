package station

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"wechat-bot/internal/fishidentity"
	"wechat-bot/internal/hostfile"
	"wechat-bot/internal/workspacefs"
)

const (
	douyinReplyMinInterval = 30 * time.Second
	douyinReplyJitter      = 30 * time.Second
	douyinReplyMaxBackoff  = 30 * time.Minute
)

type douyinReplyPacing struct {
	Next       time.Time `json:"next"`
	RateLimits uint8     `json:"rate_limits"`
}

type douyinReplyPaceAction uint8

const (
	douyinReplyCheck douyinReplyPaceAction = iota
	douyinReplyReserve
	douyinReplyRateLimited
)

type douyinReplyCooling struct{ until time.Time }

func (e *douyinReplyCooling) Error() string { return "douyin reply cooldown" }

// replyPacing shares one durable window across reply jobs in this saved
// profile. Changing individual grants or re-confirming the same profile must
// not reset it. The browser's own lock is separate from this short file lock.
func (p *localDouyinWebInteraction) replyPacing(a localDouyinWebAccess, now time.Time, action douyinReplyPaceAction) (time.Time, error) {
	if !validLocalDouyinWebID(a.record.Profile) || !fishidentity.ValidFishID(a.record.FishID) {
		return time.Time{}, errors.New("invalid reply profile")
	}
	dir := filepath.Join(p.home.dataDir, "apps", "douyin", "reply-pacing", a.record.FishID)
	root, err := workspacefs.EnsurePath(dir)
	if err != nil {
		return time.Time{}, err
	}
	defer root.Close()
	name := a.record.Profile + ".json"
	release, err := hostfile.TryLock(filepath.Join(dir, a.record.Profile+".lock"))
	if err != nil {
		return time.Time{}, err
	}
	defer release()
	state := douyinReplyPacing{}
	f, err := workspacefs.OpenRegular(root, name)
	switch {
	case err == nil:
		data, err := io.ReadAll(io.LimitReader(f, 1025))
		f.Close()
		if err != nil || len(data) > 1024 || strictLocalMemoryDecode(data, &state) != nil || state.Next.IsZero() || state.RateLimits > 6 {
			return time.Time{}, errors.New("invalid reply pacing record")
		}
	case !errors.Is(err, os.ErrNotExist):
		return time.Time{}, err
	}
	if action == douyinReplyCheck || action == douyinReplyReserve && now.Before(state.Next) {
		if now.Before(state.Next) {
			return state.Next, &douyinReplyCooling{until: state.Next}
		}
		return time.Time{}, nil
	}
	random, err := rand.Int(rand.Reader, big.NewInt(int64(douyinReplyJitter/time.Second)+1))
	if err != nil {
		return time.Time{}, err
	}
	delay := douyinReplyMinInterval + time.Duration(random.Int64())*time.Second
	if action == douyinReplyRateLimited {
		// Count adjacent platform restrictions. After an idle period the
		// ordinary spacing applies and a later restriction starts a new run.
		if now.Sub(state.Next) > douyinReplyMaxBackoff {
			state.RateLimits = 0
		}
		state.RateLimits = min(6, state.RateLimits+1)
		delay = min(douyinReplyMaxBackoff, delay+(time.Minute<<(state.RateLimits-1)))
	} else if now.Sub(state.Next) > douyinReplyMaxBackoff {
		state.RateLimits = 0
	}
	until := now.Add(delay).UTC()
	if until.After(state.Next) {
		state.Next = until
	}
	data, err := json.Marshal(state)
	if err != nil {
		return time.Time{}, err
	}
	if err := hostfile.WritePrivate(filepath.Join(dir, name), data); err != nil {
		return time.Time{}, err
	}
	return state.Next, nil
}
