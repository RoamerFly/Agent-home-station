package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	mathrand "math/rand"
	"os"
	"time"

	"wechat-bot/internal/deviceprofile"
	"wechat-bot/internal/loginflow"
	"wechat-bot/internal/loginwire"
	"wechat-bot/internal/sae08"
	"wechat-bot/internal/wechatnet"
)

// ProfileCheckpoint is private local state, not a credential exchange format.
// Token material is written here, never emitted in command output.
type ProfileCheckpoint struct {
	Profile          deviceprofile.Profile    `json:"profile"`
	LoginDeviceBytes []byte                   `json:"login_device_bytes"`
	LoginIDRule      string                   `json:"login_id_rule"`
	CreatedAt        int64                    `json:"created_at"`
	TokenAttemptedAt int64                    `json:"token_attempted_at,omitempty"`
	Token            *loginwire.TrustResponse `json:"token,omitempty"`
	TokenHistory     []TokenCheckpoint        `json:"token_history,omitempty"`
	WireProfile      *loginflow.WireProfile   `json:"wire_profile,omitempty"`
	Upgrade          *UpgradeRecord           `json:"upgrade,omitempty"`
}

type TokenCheckpoint struct {
	AttemptedAt int64                    `json:"attempted_at"`
	Token       *loginwire.TrustResponse `json:"token"`
}

func profileCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("profile", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	device := f.String("device", "secrets/device.bin", "persisted QR device bytes")
	state := f.String("state", "secrets/device-profile.session.json", "new private profile checkpoint")
	if err := f.Parse(args); err != nil {
		return errors.New("invalid profile arguments")
	}
	if _, err := os.Stat(*state); !errors.Is(err, os.ErrNotExist) {
		return errors.New("profile output must not already exist")
	}
	id, err := deviceID(*device)
	if err != nil {
		return err
	}
	now := time.Now()
	location := time.FixedZone("Asia/Shanghai", 8*3600)
	// GenIpadLoginData preserves the hex string and decodes it into the raw QR
	// bytes; keeping this pair preserves our already tested login identity.
	p, err := deviceprofile.Generate(deviceprofile.GenerateOptions{LoginDeviceID: hex.EncodeToString(id), Now: now, Location: location, UUIDEntropy: rand.Reader, Random: mathrand.New(mathrand.NewSource(now.UnixNano()))})
	if err != nil {
		return err
	}
	s := ProfileCheckpoint{Profile: p, LoginDeviceBytes: id, LoginIDRule: "hex-of-existing-16-byte-QR-device-id", CreatedAt: now.Unix()}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = writeNew(*state, b); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "profile_saved", "state": *state, "account_logged_in": false, "device_token_received": false, "login_identity_preserved": true})
}

func loadProfileCheckpoint(path string) (ProfileCheckpoint, error) {
	var s ProfileCheckpoint
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 1<<20 {
		return s, errors.New("invalid profile checkpoint file")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&s); err != nil {
		return s, errors.New("invalid profile checkpoint JSON")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return s, errors.New("profile checkpoint must contain one JSON object")
	}
	if err = s.Profile.ValidateFingerprint(); err != nil {
		return s, err
	}
	if s.WireProfile != nil {
		if err = s.WireProfile.Validate(); err != nil {
			return s, err
		}
		if s.WireProfile.Version != s.Profile.ClientVersion || s.WireProfile.DeviceType != s.Profile.DeviceType {
			return s, errors.New("profile protocol settings disagree with device fingerprint")
		}
	}
	if len(s.LoginDeviceBytes) != 16 || s.LoginDeviceBytes[0] != 0x49 || s.Profile.LoginDeviceID != hex.EncodeToString(s.LoginDeviceBytes) || s.LoginIDRule != "hex-of-existing-16-byte-QR-device-id" {
		return s, errors.New("profile login identities are inconsistent")
	}
	return s, nil
}

func saveProfileCheckpoint(path string, s ProfileCheckpoint) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return errors.New("cannot open profile checkpoint for update")
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func validateDeviceToken(s ProfileCheckpoint) error {
	if s.Token == nil || s.Token.Token == nil || len(*s.Token.Token) == 0 || s.Token.Timestamp == nil || s.TokenAttemptedAt == 0 {
		return errors.New("profile has no completed device-token response")
	}
	code, err := s.Token.Code()
	if err != nil || code != 0 {
		return errors.New("profile device-token response is not successful")
	}
	return nil
}

func fingerprintCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("fingerprint", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	state := f.String("state", "secrets/device-profile.session.json", "private profile checkpoint")
	tableFile := f.String("tables", "", "explicit pinned 08sae.dat data file")
	host := f.String("host", "short.weixin.qq.com", "WeChat hostname")
	refresh := f.Bool("refresh", false, "explicit single refresh after a completed token response")
	if err := f.Parse(args); err != nil {
		return errors.New("invalid fingerprint arguments")
	}
	if *tableFile == "" {
		return errors.New("fingerprint requires an explicit table file")
	}
	s, err := loadProfileCheckpoint(*state)
	if err != nil {
		return err
	}
	if s.TokenAttemptedAt != 0 {
		if !*refresh || validateDeviceToken(s) != nil {
			return errors.New("fingerprint already attempted; an explicit refresh requires a completed prior response")
		}
		if len(s.TokenHistory) >= 4 {
			return errors.New("local token refresh bound reached")
		}
		s.TokenHistory = append(s.TokenHistory, TokenCheckpoint{AttemptedAt: s.TokenAttemptedAt, Token: s.Token})
		s.Token = nil
	}
	b, err := os.ReadFile(*tableFile)
	if err != nil || len(b) > 512<<10 {
		return errors.New("invalid fingerprint table file")
	}
	cipher, err := sae08.ParsePinned(b)
	if err != nil {
		return err
	}
	key := make([]byte, 16)
	if _, err = io.ReadFull(rand.Reader, key); err != nil {
		return err
	}
	in, err := loginflow.FingerprintFromProfile(s.Profile, key, uint32(time.Now().Unix()))
	if err != nil {
		return err
	}
	if s.WireProfile != nil {
		in.WireProfile = s.WireProfile
	}
	carrier, err := wechatnet.New(*host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	session, err := carrier.Handshake(ctx)
	if err != nil {
		return err
	}
	s.TokenAttemptedAt = time.Now().Unix()
	if err = saveProfileCheckpoint(*state, s); err != nil {
		return errors.New("cannot record fingerprint attempt; request not sent")
	}
	token, err := loginflow.FetchDeviceToken(ctx, carrier, session, cipher, in)
	if err != nil {
		return err
	}
	s.Token = &token
	if err = saveProfileCheckpoint(*state, s); err != nil {
		return errors.New("device token received but cannot save private checkpoint; not retried")
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "device_token_saved", "state": *state, "account_logged_in": false, "message_sent": false, "experimental_handshake": session.Experimental})
}
