// wechat-send prepares recovered protobuf text requests or submits a request to
// a local wechat-sendd instance. It never connects directly to WeChat.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wechat-bot/internal/sending"
	"wechat-bot/internal/wechatwire"
)

const (
	defaultSenderURL = "http://127.0.0.1:8787"
	clientTimeout    = 15 * time.Second
	maxResponseBytes = 16 << 10
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "wechat-send:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, _ io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: wechat-send prepare|send [flags]")
	}
	switch args[0] {
	case "prepare":
		return prepare(args[1:], stdout)
	case "send":
		return send(args[1:], stdout)
	default:
		return errors.New("usage: wechat-send prepare|send [flags]")
	}
}

func prepare(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("prepare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	to := flags.String("to", "", "recipient ID")
	text := flags.String("text", "", "text content")
	out := flags.String("out", "", "new protobuf output file")
	if err := flags.Parse(args); err != nil {
		return errors.New("usage: wechat-send prepare -to RECIPIENT -text TEXT -out FILE")
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	message, err := wechatwire.NewText(*to, *text, time.Now())
	if err != nil {
		return errors.New("invalid text request")
	}
	payload, err := wechatwire.MarshalText(message)
	if err != nil {
		return errors.New("invalid text request")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil && filepath.Dir(*out) != "." {
		return errors.New("cannot create output directory")
	}
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("output file must not already exist")
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("could not write prepared protobuf")
	}
	fmt.Fprintf(stdout, "prepared %d-byte protobuf request; no message was sent\n", len(payload))
	return nil
}

func send(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	to := flags.String("to", "", "recipient ID")
	text := flags.String("text", "", "text content")
	if err := flags.Parse(args); err != nil {
		return errors.New("usage: wechat-send send -to RECIPIENT -text TEXT")
	}
	if strings.TrimSpace(*to) == "" || strings.TrimSpace(*text) == "" {
		return errors.New("-to and -text must be nonblank")
	}
	token := os.Getenv("WECHAT_API_TOKEN")
	if len(token) < 24 {
		return errors.New("WECHAT_API_TOKEN must be configured and at least 24 bytes")
	}
	base := os.Getenv("WECHAT_SENDER_URL")
	if base == "" {
		base = defaultSenderURL
	}
	endpoint, err := senderEndpoint(base)
	if err != nil {
		return errors.New("WECHAT_SENDER_URL must be an HTTP(S) URL")
	}
	body, err := json.Marshal(sending.TextRequest{Recipient: *to, Text: *text})
	if err != nil {
		return errors.New("could not encode request")
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("could not create local API request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: clientTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("local API request failed; message outcome is unknown")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return responseError(response)
	}
	ack, err := parseAcceptedReceipt(response.Body)
	if err != nil {
		return errors.New("local API returned an invalid acceptance acknowledgement; message outcome is unknown; do not retry automatically")
	}
	if ack.Status != "accepted" || ack.Recipient != *to || !nonzeroUint64(ack.ClientMessageID) || !nonzeroUint64(ack.ServerMessageID) {
		return errors.New("local API returned an incomplete acceptance acknowledgement; message outcome is unknown; do not retry automatically")
	}
	fmt.Fprintln(stdout, "accepted by local backend; recipient delivery is not asserted")
	return nil
}

type acceptedReceipt struct {
	Status          string `json:"status"`
	Recipient       string `json:"recipient"`
	ClientMessageID string `json:"client_message_id"`
	ServerMessageID string `json:"server_message_id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func parseAcceptedReceipt(body io.Reader) (acceptedReceipt, error) {
	decoder := json.NewDecoder(io.LimitReader(body, maxResponseBytes))
	decoder.DisallowUnknownFields()
	var receipt acceptedReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return acceptedReceipt{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return acceptedReceipt{}, errors.New("multiple JSON values")
	}
	return receipt, nil
}

func nonzeroUint64(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed != 0
}

func responseError(response *http.Response) error {
	var apiError errorResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes))
	_ = decoder.Decode(&apiError)
	if response.StatusCode == http.StatusGatewayTimeout || apiError.Error == "outcome_unknown" {
		return errors.New("local API reported an unknown message outcome; do not retry automatically")
	}
	if response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError {
		return fmt.Errorf("local API rejected the request (HTTP %d)", response.StatusCode)
	}
	return fmt.Errorf("local API did not confirm message acceptance (HTTP %d); message outcome is unknown; do not retry automatically", response.StatusCode)
}

func senderEndpoint(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid sender URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/messages/text"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
