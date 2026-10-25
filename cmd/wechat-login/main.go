// wechat-login performs bounded steps of our reconstructed login flow.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"wechat-bot/internal/mmtls"
	"wechat-bot/internal/wechatnet"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "wechat-login:", err)
		os.Exit(1)
	}
}
func run(args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "upgrade" {
		return upgradeCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "initialize" {
		return initializeCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "profile" {
		return profileCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "fingerprint" {
		return fingerprintCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "qr" {
		return qrCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "poll" {
		return pollCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "authorize" {
		return authorizeCommand(args[1:], out)
	}
	if len(args) == 0 || args[0] != "probe" {
		return errors.New("usage: wechat-login profile|upgrade|fingerprint|probe|qr|poll|authorize|initialize [options]; poll -wait 5m watches until confirmation")
	}
	f := flag.NewFlagSet("probe", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	host := f.String("host", "short.weixin.qq.com", "WeChat short hostname")
	file := f.String("out", "", "new diagnostic summary file; never contains session keys")
	if err := f.Parse(args[1:]); err != nil {
		return errors.New("invalid probe arguments")
	}
	c, err := wechatnet.New(*host)
	if err != nil {
		return err
	}
	now := time.Now()
	h, err := mmtls.NewHandshake(now, rand.Reader)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, networkErr := c.PostRecord(ctx, h.RequestBody(), now)
	type summary struct {
		Scope                       string `json:"scope"`
		Host                        string `json:"host"`
		Time                        string `json:"time"`
		RequestBytes                int    `json:"request_bytes"`
		ResponseBytes               int    `json:"response_bytes"`
		ResponseSHA256              string `json:"response_sha256,omitempty"`
		RecordTypes                 []int  `json:"record_types,omitempty"`
		RecordLengths               []int  `json:"record_lengths,omitempty"`
		CarrierError                string `json:"carrier_error,omitempty"`
		HandshakeError              string `json:"handshake_error,omitempty"`
		KeyExchangeVerified         bool   `json:"key_exchange_verified"`
		ServerIdentityAuthenticated bool   `json:"server_identity_authenticated"`
		AccountLoggedIn             bool   `json:"account_logged_in"`
	}
	s := summary{Scope: "initial public-key MMTLS handshake only; no account login or message", Host: *host, Time: now.UTC().Format(time.RFC3339), RequestBytes: len(h.RequestBody()), ResponseBytes: len(raw)}
	if networkErr != nil {
		s.CarrierError = networkErr.Error()
	} else {
		sum := sha256.Sum256(raw)
		s.ResponseSHA256 = hex.EncodeToString(sum[:])
		for rest := raw; len(rest) >= 5; {
			n := int(binary.BigEndian.Uint16(rest[3:5]))
			if n > len(rest)-5 {
				break
			}
			s.RecordTypes = append(s.RecordTypes, int(rest[0]))
			s.RecordLengths = append(s.RecordLengths, n)
			rest = rest[5+n:]
		}
		session, err := h.Complete(raw)
		if err != nil {
			s.HandshakeError = err.Error()
		} else {
			s.KeyExchangeVerified = true
			s.ServerIdentityAuthenticated = !session.Experimental
		}
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if *file != "" {
		f, err := os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("diagnostic output must be a new file")
		}
		_, e := f.Write(b)
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return errors.New("cannot save diagnostics")
		}
	}
	fmt.Fprintln(out, string(b))
	if networkErr != nil {
		return errors.New("MMTLS probe carrier failed")
	}
	if !s.KeyExchangeVerified {
		return errors.New("MMTLS probe response not verified")
	}
	return nil
}
