// Command agent-home-station starts the local, application-independent home
// station for 大肥鱼. It deliberately has no dependency on a WeChat account,
// native bridge, or model service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"wechat-bot/internal/admin"
)

const defaultListen = "127.0.0.1:8082"

type config struct {
	dataDir       string
	workspaceRoot string
	listen        string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		// This path handles only local configuration and listener errors, never
		// provider responses or credential values. Keep the actual failure useful.
		log.Printf("Agent-home-station 无法启动：%v", err)
		os.Exit(1)
	}
}

// run is deliberately independent from the working directory. It is kept
// separate from main so desktop integration can stop it using a context and
// tests can validate the command without signals.
func run(ctx context.Context, args []string, output io.Writer) error {
	path, err := os.Executable()
	if err != nil {
		return errors.New("cannot determine executable location")
	}
	cfg, err := parseConfig(args, path)
	if err != nil {
		return err
	}

	password, err := admin.Password(cfg.dataDir, os.Getenv("AHS_ADMIN_PASSWORD"))
	if err != nil {
		return err
	}
	handler, err := admin.NewLocalHomeHandler(cfg.dataDir, cfg.workspaceRoot, password, "")
	if err != nil {
		return err
	}
	defer handler.Close()

	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()

	fmt.Fprintf(output, "Agent Home Station 已启动：%s\n工作空间：%s\n", listener.Addr().String(), cfg.workspaceRoot)
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-serveResult; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func parseConfig(args []string, executable string) (config, error) {
	if executable == "" {
		return config{}, errors.New("executable path is unavailable")
	}
	fs := flag.NewFlagSet("agent-home-station", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataFlag := fs.String("data", "", "本机身份与设置目录（绝对路径）")
	workspaceFlag := fs.String("workspace", "", "大肥鱼工作空间根目录（绝对路径）")
	listenFlag := fs.String("listen", defaultListen, "仅本机监听地址")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, errors.New("unexpected positional arguments")
	}

	executable, err := filepath.Abs(executable)
	if err != nil {
		return config{}, err
	}
	dataDir := *dataFlag
	if dataDir == "" {
		dataDir = filepath.Join(filepath.Dir(executable), "data", "station")
	} else if !filepath.IsAbs(dataDir) {
		return config{}, errors.New("data directory must be absolute")
	}
	dataDir = filepath.Clean(dataDir)

	workspaceRoot := *workspaceFlag
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(dataDir, "managed-workspaces")
	} else if !filepath.IsAbs(workspaceRoot) {
		return config{}, errors.New("workspace directory must be absolute")
	}
	workspaceRoot = filepath.Clean(workspaceRoot)

	listen, err := localListenAddress(*listenFlag)
	if err != nil {
		return config{}, err
	}
	return config{dataDir: dataDir, workspaceRoot: workspaceRoot, listen: listen}, nil
}

// localListenAddress accepts only unambiguous loopback addresses. In
// particular, a host name other than localhost cannot silently resolve to a
// network interface later.
func localListenAddress(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || port == "" {
		return "", errors.New("listen address must include a loopback host and port")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", errors.New("listen port is invalid")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", errors.New("listen address must be loopback only")
		}
	}
	// Preserve localhost for its intended local-only behaviour and normalize IP
	// spellings so callers can safely show the effective endpoint.
	if host == "localhost" {
		return net.JoinHostPort(host, strconv.FormatUint(portNumber, 10)), nil
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.FormatUint(portNumber, 10)), nil
}
