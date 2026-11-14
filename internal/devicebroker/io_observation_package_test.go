package devicebroker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func scopedObservationOptions(t *testing.T) Options {
	t.Helper()
	o, paths, _, _ := mockIOOptions(t)
	p := validProfile(paths.adb)
	d := p.Devices["allowed"]
	d.Packages = append(d.Packages, "com.example.other")
	p.Devices["allowed"] = d
	writeJSON(t, paths.config, p)
	return o
}

func TestObservationPackageRejectsForeignForegroundBeforeScreenshot(t *testing.T) {
	o := scopedObservationOptions(t)
	base := o.Runner
	screenshots := 0
	o.Runner = func(ctx context.Context, client string, args []string, limit int64, timeout time.Duration) ([]byte, error) {
		switch strings.Join(args[6:], " ") {
		case "shell dumpsys window":
			return []byte("mCurrentFocus=Window{a u0 com.example.other/.Main}"), nil
		case "exec-out screencap -p":
			screenshots++
		}
		return base(ctx, client, args, limit, timeout)
	}
	b, err := OpenIO(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := WithObservationPackage(t.Context(), "com.example.browser")
	c, err := b.Begin(ctx, "observe", ioJSON(map[string]any{"device_id": "allowed"}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Service(ctx, "screenshot", "allowed", json.RawMessage(`{}`)); !errors.Is(err, ErrObservationAppMismatch) {
		t.Fatalf("screenshot error = %v, want %v", err, ErrObservationAppMismatch)
	}
	if screenshots != 0 {
		t.Fatalf("captured foreign app: screenshots=%d", screenshots)
	}
}

func TestObservationPackageRechecksAfterScreenshotAndOmitsUI(t *testing.T) {
	o := scopedObservationOptions(t)
	base := o.Runner
	focuses, screenshots, uiReads := 0, 0, 0
	o.Runner = func(ctx context.Context, client string, args []string, limit int64, timeout time.Duration) ([]byte, error) {
		joined := strings.Join(args[6:], " ")
		switch {
		case joined == "shell dumpsys window":
			focuses++
			if focuses == 1 {
				return []byte("mCurrentFocus=Window{a u0 com.example.browser/.Main}"), nil
			}
			return []byte("mCurrentFocus=Window{a u0 com.example.other/.Main}"), nil
		case joined == "exec-out screencap -p":
			screenshots++
		case strings.Contains(joined, "uiautomator dump"), strings.Contains(joined, "exec-out cat /sdcard/whale-ui-"):
			uiReads++
		}
		return base(ctx, client, args, limit, timeout)
	}
	b, err := OpenIO(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := WithObservationPackage(t.Context(), "com.example.browser")
	c, err := b.Begin(ctx, "observe", ioJSON(map[string]any{"device_id": "allowed"}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err = c.Service(ctx, "screenshot", "allowed", json.RawMessage(`{}`)); !errors.Is(err, ErrObservationAppMismatch) {
		t.Fatalf("screenshot error = %v, want %v", err, ErrObservationAppMismatch)
	}
	if screenshots != 1 || focuses != 2 {
		t.Fatalf("capture checks = screenshots=%d focuses=%d, want 1/2", screenshots, focuses)
	}
	if c.(*ioCall).shot != nil {
		t.Fatal("switched-app screenshot remained available")
	}
	if _, err = c.Service(ctx, "ui", "allowed", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if uiReads != 0 {
		t.Fatalf("scoped observation read foreign UI: %d", uiReads)
	}
}

func TestObservationPackageRequiresRegisteredPackage(t *testing.T) {
	o := scopedObservationOptions(t)
	b, err := OpenIO(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := WithObservationPackage(t.Context(), "com.example.unknown")
	if _, err = b.Begin(ctx, "observe", ioJSON(map[string]any{"device_id": "allowed"})); !errors.Is(err, ErrObservationAppMismatch) {
		t.Fatalf("unregistered app scope error = %v, want %v", err, ErrObservationAppMismatch)
	}
}
