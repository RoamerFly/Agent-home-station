package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"wechat-bot/internal/loginflow"
	"wechat-bot/internal/native"
	"wechat-bot/internal/wechatnet"
)

func initializeCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("initialize", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("session", "secrets/wechat.session.json", "validated account session")
	if err := f.Parse(args); err != nil {
		return errors.New("invalid initialization arguments")
	}
	// Use the sender's existing strict session validation without any network.
	if _, err := native.New(*path); err != nil {
		return err
	}
	b, err := os.ReadFile(*path)
	if err != nil || len(b) > 65536 {
		return errors.New("invalid initialization session file")
	}
	var session native.SessionFile
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&session); err != nil {
		return errors.New("invalid initialization session JSON")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errors.New("initialization session must contain one JSON object")
	}
	responsePath := *path + ".newinit-response.session.json"
	if _, err = os.Stat(responsePath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("initialization recovery output must be a new file")
	}
	opt := loginflow.InitializationOptions{Checkpoint: func(s *native.SessionFile) error {
		data, err := json.Marshal(s)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(*path, os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	}, ResponseCheckpoint: func(data []byte) error {
		record, err := json.Marshal(struct {
			Response   []byte `json:"response"`
			ReceivedAt int64  `json:"received_at"`
		}{data, time.Now().Unix()})
		if err != nil {
			return err
		}
		return writeNew(responsePath, record)
	}}
	carrier, err := wechatnet.New(session.ShortHost)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	r, err := loginflow.InitializePage(ctx, carrier, &session, opt)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "initialization_response_verified", "commands_preserved": r.Commands, "continue_flag": r.ContinueFlag, "sync_state_saved": r.Verified, "full_sync_complete": false, "message_sent": false})
}
