package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	mathrand "math/rand"
	"os"
	"time"

	"wechat-bot/internal/loginflow"
	"wechat-bot/internal/sae08"
	"wechat-bot/internal/wechatnet"
)

func authorizeCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("authorize", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	state := f.String("state", "secrets/pending.session.json", "confirmed local login checkpoint")
	session := f.String("session", "secrets/wechat.session.json", "new local account session")
	proof := f.String("device-proof", "", "optional actual WCExtInfo protobuf file")
	profilePath := f.String("profile", "", "device-token profile; defaults to path stored by qr")
	tables := f.String("tables", "", "pinned 08sae.dat used by the reconstructed proof producers")
	if err := f.Parse(args); err != nil {
		return errors.New("invalid authorize arguments")
	}
	if _, err := os.Stat(*session); !errors.Is(err, os.ErrNotExist) {
		return errors.New("account session output must not already exist")
	}
	b, err := os.ReadFile(*state)
	if err != nil || len(b) > 65536 {
		return errors.New("invalid pending checkpoint")
	}
	var pending loginflow.Pending
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&pending); err != nil {
		return errors.New("invalid pending checkpoint JSON")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errors.New("invalid pending checkpoint JSON")
	}
	if err = pending.Validate(); err != nil {
		return err
	}
	opt := loginflow.AuthorizationOptions{Checkpoint: func(p *loginflow.Pending) error {
		encoded, err := json.Marshal(p)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*state, os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(encoded)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}}
	responsePath := *session + ".auth-exchange.session.json"
	if _, err = os.Stat(responsePath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("authorization recovery output must be a new file")
	}
	opt.ResponseCheckpoint = func(exchange loginflow.AuthorizationExchange) error {
		b, err := json.Marshal(exchange)
		if err != nil {
			return err
		}
		return writeNew(responsePath, b)
	}
	if *profilePath == "" {
		*profilePath = pending.DeviceProfilePath
	}
	if *profilePath != "" {
		if *proof != "" {
			return errors.New("select a device profile or explicit proof file, not both")
		}
		if *tables == "" {
			return errors.New("profile authorization requires explicit pinned tables")
		}
		profile, err := loadProfileCheckpoint(*profilePath)
		if err != nil {
			return err
		}
		if !bytes.Equal(profile.LoginDeviceBytes, pending.DeviceID) {
			return errors.New("authorization profile differs from pending QR bytes")
		}
		if err = validateDeviceToken(profile); err != nil {
			return err
		}
		profileWire := loginflow.DefaultWireProfile()
		profileWire.Version, profileWire.DeviceType = profile.Profile.ClientVersion, profile.Profile.DeviceType
		if profile.WireProfile != nil {
			profileWire = *profile.WireProfile
		}
		pendingWire := loginflow.DefaultWireProfile()
		if pending.WireProfile != nil {
			pendingWire = *pending.WireProfile
		}
		if profileWire != pendingWire {
			return errors.New("device token and pending QR use different protocol settings")
		}
		b, err := os.ReadFile(*tables)
		if err != nil || len(b) > 512<<10 {
			return errors.New("invalid authorization table file")
		}
		cipher, err := sae08.ParsePinned(b)
		if err != nil {
			return err
		}
		opt.ExtSpamInfo, err = loginflow.AuthorizationProof(profile.Profile, string(*profile.Token.Token), pending.UserName, cipher, time.Now, mathrand.New(mathrand.NewSource(time.Now().UnixNano())))
		if err != nil {
			return err
		}
		opt.DeviceProfile = &profile.Profile
	}
	if *proof != "" {
		data, err := os.ReadFile(*proof)
		if err != nil || len(data) > 1<<20 {
			return errors.New("invalid device proof input")
		}
		opt.ExtSpamInfo = data
	}
	carrier, err := wechatnet.New(pending.Host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result, err := loginflow.Authorize(ctx, carrier, &pending, opt)
	// Persist an attempted exchange even if it failed: an unknown outcome must
	// not become an automatic second authorization request on CLI restart.
	if pending.AuthorizationAttemptedAt != 0 {
		checkpoint, saveErr := json.Marshal(pending)
		if saveErr != nil || os.WriteFile(*state, checkpoint, 0600) != nil {
			return errors.New("authorization attempted but checkpoint could not be saved; do not retry automatically")
		}
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err = writeNew(*session, encoded); err != nil {
		return err
	}
	// Remove temporary authorization credentials from the checkpoint on success.
	pending.TemporaryPassword = ""
	pending.UserName = ""
	updated, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err = os.WriteFile(*state, updated, 0600); err != nil {
		return errors.New("session saved but cannot clear temporary checkpoint credentials")
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "account_session_saved", "session": *session, "real_message_sent": false, "needs_initialization_and_send_validation": true})
}
