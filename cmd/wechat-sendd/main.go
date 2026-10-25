// wechat-sendd serves the authenticated local text-message API.
// A ready transport can acknowledge server acceptance only; it cannot prove a
// recipient received or read a message.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"wechat-bot/internal/httpapi"
	"wechat-bot/internal/linuxnative"
	"wechat-bot/internal/native"
	"wechat-bot/internal/sending"
)

const defaultListen = "127.0.0.1:8787"

func main() {
	if err := run(); err != nil {
		// Do not print a transport error: it may contain session material.
		log.Fatal("wechat-sendd: startup failed")
	}
}

func run() error {
	token := os.Getenv("WECHAT_API_TOKEN")
	if token == "" {
		return errors.New("WECHAT_API_TOKEN is required")
	}

	engine, err := newEngine()
	if err != nil {
		return errors.New("transport initialization failed")
	}
	handler, err := httpapi.New(httpapi.Config{
		Token:             token,
		AllowedRecipients: recipients(os.Getenv("WECHAT_ALLOWED_RECIPIENTS")),
		SendTimeout:       15 * time.Second,
	}, engine)
	if err != nil {
		return errors.New("HTTP API configuration failed")
	}

	listen := os.Getenv("WECHAT_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ready := engine.Ready()
	log.Printf("wechat-sendd listening on %s; transport_ready=%t", listen, ready)
	if !ready {
		log.Print("transport is not ready; a valid protocol login session is required before messages can be accepted")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-stopped
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// newEngine selects a transport only when explicitly configured. The legacy
// native transport remains the default so an unavailable Linux-native service
// can never silently send through a different protocol implementation.
func newEngine() (sending.Engine, error) {
	switch os.Getenv("WECHAT_TRANSPORT") {
	case "", "native":
		return native.New(os.Getenv("WECHAT_SESSION_FILE"))
	case "linux-native":
		return linuxnative.New(linuxnative.Config{
			URL:       os.Getenv("WECHAT_NATIVE_URL"),
			TokenFile: os.Getenv("WECHAT_NATIVE_TOKEN_FILE"),
			Account:   os.Getenv("WECHAT_NATIVE_ACCOUNT"),
		})
	default:
		return nil, errors.New("unsupported WECHAT_TRANSPORT")
	}
}

func recipients(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
