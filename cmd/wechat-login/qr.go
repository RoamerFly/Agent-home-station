package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"time"

	"wechat-bot/internal/loginflow"
	"wechat-bot/internal/wechatnet"
)

func qrCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("qr", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	host := f.String("host", "short.weixin.qq.com", "WeChat short hostname")
	state := f.String("state", "secrets/pending.session.json", "new local secret checkpoint")
	imagePath := f.String("image", "secrets/login.png", "new local QR image")
	devicePath := f.String("device", "secrets/device.bin", "persistent 16-byte device identifier")
	profilePath := f.String("profile", "", "optional ready device-token profile checkpoint")
	if err := f.Parse(args); err != nil {
		return errors.New("invalid qr arguments")
	}
	for _, p := range []string{*state, *imagePath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			return errors.New("QR image and pending state outputs must not already exist")
		}
	}
	device, err := deviceID(*devicePath)
	if err != nil {
		return err
	}
	wire := loginflow.DefaultWireProfile()
	if *profilePath != "" {
		profile, err := loadProfileCheckpoint(*profilePath)
		if err != nil {
			return err
		}
		if !bytes.Equal(profile.LoginDeviceBytes, device) {
			return errors.New("device profile differs from QR device bytes")
		}
		if err = validateDeviceToken(profile); err != nil {
			return err
		}
		wire.Version, wire.DeviceType = profile.Profile.ClientVersion, profile.Profile.DeviceType
		if profile.WireProfile != nil {
			wire = *profile.WireProfile
		}
	}
	carrier, err := wechatnet.New(*host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pending, encoded, err := loginflow.GetQRWithProfile(ctx, carrier, *host, device, wire)
	if err != nil {
		return err
	}
	pending.DeviceProfilePath = *profilePath
	config, format, err := image.DecodeConfig(bytes.NewReader(encoded))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 2048 || config.Height > 2048 {
		return errors.New("invalid QR image response")
	}
	img, _, err := image.Decode(bytes.NewReader(encoded))
	if err != nil {
		return errors.New("cannot decode QR image")
	}
	var pngData bytes.Buffer
	if err = png.Encode(&pngData, img); err != nil {
		return err
	}
	stateData, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	if err = writeNew(*imagePath, pngData.Bytes()); err != nil {
		return err
	}
	if err = writeNew(*state, stateData); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "qr_ready", "image": *imagePath, "state": *state, "source_image_format": format, "width": config.Width, "height": config.Height, "expires_at": pending.ExpiresAt, "account_logged_in": false, "experimental_handshake": pending.MMTLS.Experimental})
}

func pollCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("poll", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	state := f.String("state", "secrets/pending.session.json", "local secret checkpoint")
	wait := f.Duration("wait", 0, "wait for confirmation, at most 10m; errors are never retried")
	if err := f.Parse(args); err != nil {
		return errors.New("invalid poll arguments")
	}
	if *wait < 0 || *wait > 10*time.Minute {
		return errors.New("poll wait must be between 0 and 10m")
	}
	deadline := time.Now().Add(*wait)
	for {
		status, err := pollOnce(*state, out)
		if err != nil {
			return err
		}
		if status != 0 && status != 1 || !time.Now().Before(deadline) {
			return nil
		}
		delay := time.Until(deadline)
		if delay > 3*time.Second {
			delay = 3 * time.Second
		}
		time.Sleep(delay)
	}
}

func pollOnce(state string, out io.Writer) (uint32, error) {
	data, err := os.ReadFile(state)
	if err != nil || len(data) > 65536 {
		return 0, errors.New("invalid pending state file")
	}
	var p loginflow.Pending
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return 0, errors.New("invalid pending login JSON")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return 0, errors.New("invalid pending login JSON")
	}
	if err = p.Validate(); err != nil {
		return 0, err
	}
	c, err := wechatnet.New(p.Host)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	status, err := loginflow.Poll(ctx, c, &p)
	if err != nil {
		return 0, err
	}
	updated, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	// Replace only our own explicitly named checkpoint. It contains secrets,
	// so do not print it or broaden file permissions during the update.
	if err = os.WriteFile(state, updated, 0600); err != nil {
		return 0, errors.New("cannot update pending checkpoint")
	}
	return status, json.NewEncoder(out).Encode(map[string]any{"qr_status": status, "scan_confirmed": status == 2, "account_session_created": false})
}

func deviceID(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != 16 || b[0] != 0x49 {
			return nil, errors.New("invalid persisted device identifier")
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("cannot read persisted device identifier")
	}
	b = make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		return nil, err
	}
	b[0] = 0x49
	if err = writeNew(path, b); err != nil {
		return nil, err
	}
	return b, nil
}
func writeNew(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("cannot create private output directory")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("output must be a new file: %s", path)
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot write output")
	}
	return nil
}
