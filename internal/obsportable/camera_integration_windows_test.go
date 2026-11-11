//go:build windows

package obsportable

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/bluenviron/gortmplib"
	"github.com/bluenviron/gortmplib/pkg/message"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"

	"wechat-bot/internal/obspreview"
	"wechat-bot/internal/rtmprelay"
)

func TestOfficialOBSPreviewReadsProgramAndAudio(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	installation, media := installOfficialTestProgram(t, ctx)
	defer media.Source.Close()
	root, err := os.OpenRoot(installation.studio)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeProfileWithMedia(ctx, root, installation.studio, 1, "fixture-relay-ticket", &ownedMedia{Media: *media}); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(filepath.FromSlash("config/obs-studio/basic/profiles/" + profileName + "/service.json")); err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := socket.Addr().(*net.TCPAddr).Port
	if err := socket.Close(); err != nil {
		t.Fatal(err)
	}
	password := uuid.NewString()
	config, err := json.Marshal(map[string]any{"first_load": false, "server_enabled": true, "server_port": port, "auth_required": true, "server_password": password, "alerts_enabled": false})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(filepath.FromSlash("config/obs-studio/plugin_config/obs-websocket/config.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(installation.studio, filepath.FromSlash(executable))
	args := cameraArguments()
	process, err := obspreview.LaunchPortableEncoderProcess(ctx, obspreview.EncoderLaunch{
		Executable: exe, Directory: filepath.Dir(exe), SHA256: installation.files[executable], Arguments: args[:len(args)-1],
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := process.Stop(stop); err != nil {
			t.Errorf("release isolated OBS: %v", err)
		}
	}()
	read, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	for {
		frame, err := obspreview.CaptureWithStatus(read, port, password)
		if err == nil {
			status := frame.Status
			if frame.Scene != "Station Program" || frame.ImageData == "" || status == nil ||
				status.VirtualCamera == nil || *status.VirtualCamera || status.Streaming == nil || status.Streaming.Active ||
				status.Recording == nil || *status.Recording || status.Audio == nil || !status.Audio.Complete {
				t.Fatalf("isolated OBS program/status not confirmed: scene=%q status=%+v", frame.Scene, status)
			}
			for _, input := range status.Audio.Inputs {
				if input.Name == "Station Media" && !input.Muted && input.Volume != nil && *input.Volume == 1 {
					return
				}
			}
			t.Fatalf("isolated media audio input not observed: %+v", status.Audio.Inputs)
		}
		select {
		case <-read.Done():
			t.Fatalf("isolated OBS status could not be read: %v", err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func installOfficialTestProgram(t *testing.T, ctx context.Context) (*Installation, *Media) {
	t.Helper()
	archive := os.Getenv("AHS_TEST_OBS_ZIP")
	if archive == "" {
		t.Skip("set AHS_TEST_OBS_ZIP to run the real Windows OBS test")
	}
	ffmpeg := os.Getenv("AHS_TEST_OBS_FFMPEG")
	if !filepath.IsAbs(archive) || !filepath.IsAbs(ffmpeg) {
		t.Fatal("AHS_TEST_OBS_ZIP and AHS_TEST_OBS_FFMPEG must be absolute paths")
	}
	base := t.TempDir()
	station, workspace := filepath.Join(base, "station"), filepath.Join(base, "home")
	for _, dir := range []string{station, workspace} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := Install(ctx, station, workspace, archive); err != nil {
		t.Fatalf("install official OBS: %v", err)
	}
	installation, err := Open(ctx, station, workspace)
	if err != nil {
		t.Fatalf("open installed OBS: %v", err)
	}
	program := filepath.Join(workspace, "program.mp4")
	command := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=black:s=1280x720:r=30:d=1,drawbox=x=0:y=0:w=640:h=720:c=white:t=fill",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", program)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic program: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(program)
	if err != nil {
		t.Fatal(err)
	}
	return installation, &Media{Source: source, SHA256: fmt.Sprintf("%x", sha256.Sum256(raw)),
		Bytes: int64(len(raw)), Width: 1280, Height: 720, Loop: true}
}

// Opt-in to exercise the official runtime without an application account.
func TestOfficialOBSProgramCamera(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	installation, media := installOfficialTestProgram(t, ctx)
	camera, err := installation.CameraWithMedia(ctx, media)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := camera.Stop(stop); err != nil {
			t.Errorf("release test camera: %v", err)
		}
	})
	start, endStart := context.WithTimeout(ctx, 25*time.Second)
	err = camera.Start(start)
	endStart()
	if errors.Is(err, ErrCameraNotRegistered) {
		if _, err := os.Stat(filepath.Join(installation.studio, "config")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unregistered camera wrote a profile: %v", err)
		}
		t.Skip("portable install and missing-driver preflight verified; Windows camera class is not registered")
	}
	if err != nil {
		t.Fatalf("start private OBS camera: %v", err)
	}
	if err := camera.Check(ctx); err != nil {
		t.Fatalf("real frame queue did not advance: %v", err)
	}
	if err := checkOfficialOBSProgramFrame(ctx); err != nil {
		t.Fatal(err)
	}
	if err := checkDirectShowProgramFrame(ctx, os.Getenv("AHS_TEST_OBS_FFMPEG")); err != nil {
		t.Fatal(err)
	}
	stop, endStop := context.WithTimeout(ctx, 5*time.Second)
	defer endStop()
	if err := camera.Stop(stop); err != nil {
		t.Fatalf("stop private OBS camera: %v", err)
	}
	if err := requireCameraMappingAbsent(cameraMapping); err != nil {
		t.Fatalf("virtual camera remained after stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installation.studio, "config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private program profile remained after stop: %v", err)
	}
}

// A real Windows consumer must receive the same picture as the OBS producer.
// Selecting this exact software device never opens a physical camera.
func checkDirectShowProgramFrame(ctx context.Context, ffmpeg string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "dshow", "-video_size", "1280x720", "-framerate", "30", "-i", "video=OBS Virtual Camera",
		"-frames:v", "15", "-vf", "scale=4:2:flags=area", "-pix_fmt", "gray", "-c:v", "rawvideo", "-threads", "1", "-f", "rawvideo", "pipe:1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	pixels, err := cmd.Output()
	if err != nil {
		var failed *exec.ExitError
		if errors.As(err, &failed) {
			return fmt.Errorf("read OBS DirectShow device: %w %.300s", err, failed.Stderr)
		}
		return err
	}
	const frameBytes = 4 * 2
	for offset := 0; offset+frameBytes <= len(pixels); offset += frameBytes {
		frame := pixels[offset : offset+frameBytes]
		if frame[0] > 180 && frame[3] < 60 && frame[4] > 180 && frame[7] < 60 {
			return nil
		}
	}
	return fmt.Errorf("DirectShow consumer received %d bytes without the black/white program", len(pixels))
}

func TestOfficialOBSProgramLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	installation, media := installOfficialTestProgram(t, ctx)
	encoder, err := installation.EncoderWithMedia(ctx, media)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := encoder.Stop(stop); err != nil {
			t.Errorf("stop test encoder: %v", err)
		}
	}()
	sink, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	signal, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	deadline, _ := signal.Deadline()
	if err := sink.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	received := make(chan error, 1)
	go func() { received <- readTestProgramPackets(sink, deadline) }()
	destination := &url.URL{Scheme: "rtmp", Host: sink.Addr().String(), Path: "/test", Fragment: "local-only"}
	relay, err := rtmprelay.Open(signal, rtmprelay.Config{Destination: destination, VerifyPeer: encoder.VerifyPublisher,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != sink.Addr().String() {
				return nil, errors.New("test only permits the local RTMP sink")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := relay.Close(closeCtx); err != nil {
			t.Errorf("close test relay: %v", err)
		}
	}()
	if err := encoder.Start(signal, relay.Port(), relay.Ticket()); err != nil {
		t.Fatal(err)
	}
	if err := <-received; err != nil {
		logs, _ := filepath.Glob(filepath.Join(installation.studio, "config", "obs-studio", "logs", "*.txt"))
		for _, name := range logs {
			data, _ := os.ReadFile(name)
			if len(data) > 3500 {
				data = data[len(data)-3500:]
			}
			t.Logf("isolated OBS log tail:\n%s", data)
		}
		t.Fatalf("real OBS audio/video did not reach the local RTMP sink (relay %v): %v", relay.Status(), err)
	}
}

func readTestProgramPackets(sink *net.TCPListener, deadline time.Time) error {
	conn, err := sink.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	server := &gortmplib.ServerConn{RW: conn}
	if err := server.Initialize(); err != nil {
		return err
	}
	if err := server.AcceptConn(); err != nil {
		return err
	}
	if !server.Publish || server.URL.Path != "/test/local-only" {
		return errors.New("unexpected RTMP publication")
	}
	if err := server.AcceptAction(); err != nil {
		return err
	}
	var audio, video bool
	for !audio || !video {
		msg, err := server.Read()
		if err != nil {
			return fmt.Errorf("reading packets (audio %t, video %t): %w", audio, video, err)
		}
		switch m := msg.(type) {
		case *message.Audio:
			audio = audio || m.AACType == message.AudioAACTypeAU && len(m.AU) > 0
		case *message.Video:
			video = video || m.Codec == message.CodecH264 && m.PacketType == message.VideoPacketTypeAU && len(m.AU) > 0
		}
	}
	return nil
}

// Read two luminance bytes in the validated OBS 33.0.1 NV12 queue. A moving
// counter by itself can also be a black frame from a missing media source.
func checkOfficialOBSProgramFrame(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const width, height = 1280, 720
	const stride = width*height*3/2 + 32
	handle, err := openCameraMapping(cameraMapping)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	view, err := windows.MapViewOfFile(handle, windows.FILE_MAP_READ, 0, 0, 96+3*stride)
	if err != nil {
		return err
	}
	defer windows.UnmapViewOfFile(view)
	base := cameraViewPointer(view)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var left, right byte
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("synthetic white/black program not observed (luma %d/%d): %w", left, right, err)
		}
		counter := (*uint32)(unsafe.Add(base, 4))
		frame := atomic.LoadUint32(counter)
		y := uintptr(96+(frame%3)*stride+32) + width*(height/2)
		left = *(*byte)(unsafe.Add(base, y+width/4))
		right = *(*byte)(unsafe.Add(base, y+width*3/4))
		if atomic.LoadUint32(counter) == frame && left > 180 && right < 60 {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}
