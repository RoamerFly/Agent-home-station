package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"wechat-bot/internal/loginflow"
)

// UpgradeCandidate names the protocol settings being tested and retains their
// source identity. Provenance records where settings came from, not successful
// interoperability. No credentials belong in this public configuration.
type UpgradeCandidate struct {
	Schema            string                `json:"schema"`
	Name              string                `json:"name"`
	SourceURL         string                `json:"source_url"`
	ArtifactSHA256    string                `json:"artifact_sha256"`
	ChangeScope       string                `json:"change_scope,omitempty"`
	CompatibilityNote string                `json:"compatibility_note,omitempty"`
	Wire              loginflow.WireProfile `json:"wire"`
}

type UpgradeRecord struct {
	CandidatePath   string `json:"candidate_path"`
	CandidateSHA256 string `json:"candidate_sha256"`
	PreviousVersion uint32 `json:"previous_version"`
	CreatedAt       int64  `json:"created_at"`
}

func readUpgradeCandidate(path string) (UpgradeCandidate, string, error) {
	var c UpgradeCandidate
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 16384 {
		return c, "", errors.New("invalid upgrade candidate file")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, "", errors.New("invalid upgrade candidate JSON")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return c, "", errors.New("upgrade candidate must contain one object")
	}
	digest, err := hex.DecodeString(c.ArtifactSHA256)
	if c.Schema != "wechat-ipad-upgrade-candidate-1" || len(c.Name) == 0 || len(c.Name) > 128 || len(c.SourceURL) == 0 || len(c.SourceURL) > 2048 || err != nil || len(digest) != 32 {
		return c, "", errors.New("upgrade candidate lacks bounded source identity")
	}
	if err = c.Wire.Validate(); err != nil {
		return c, "", err
	}
	sum := sha256.Sum256(b)
	return c, hex.EncodeToString(sum[:]), nil
}

// upgrade writes a new private profile while preserving the established device
// identity and statistics. Old tokens are not carried into a changed version:
// the caller must obtain one new server-issued token before creating a QR.
func upgradeCommand(args []string, out io.Writer) error {
	f := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	from := f.String("from", "secrets/device-profile.session.json", "existing private device profile")
	to := f.String("out", "", "new private profile; original remains unchanged")
	candidate := f.String("candidate", "", "public named upgrade candidate JSON")
	if err := f.Parse(args); err != nil || *to == "" || *candidate == "" || f.NArg() != 0 {
		return errors.New("usage: wechat-login upgrade -from <profile> -out <new-profile> -candidate <candidate.json>")
	}
	if _, err := os.Stat(*to); !errors.Is(err, os.ErrNotExist) {
		return errors.New("upgraded profile output must not already exist")
	}
	c, digest, err := readUpgradeCandidate(*candidate)
	if err != nil {
		return err
	}
	s, err := loadProfileCheckpoint(*from)
	if err != nil {
		return err
	}
	if c.Wire.Version <= s.Profile.ClientVersion {
		return errors.New("upgrade candidate must have a newer client version")
	}
	if c.Wire.DeviceType != s.Profile.DeviceType {
		return errors.New("this migration requires the same device type; a different type needs a matching fingerprint/proof implementation")
	}
	previous := s.Profile.ClientVersion
	s.Profile.ClientVersion = c.Wire.Version
	s.WireProfile = &c.Wire
	s.Upgrade = &UpgradeRecord{CandidatePath: *candidate, CandidateSHA256: digest, PreviousVersion: previous, CreatedAt: time.Now().Unix()}
	s.Token, s.TokenHistory, s.TokenAttemptedAt = nil, nil, 0
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = writeNew(*to, b); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{"status": "upgrade_candidate_saved", "state": *to, "candidate": c.Name, "previous_version": previous, "client_version": c.Wire.Version, "login_identity_preserved": true, "requires_new_device_token": true, "account_logged_in": false, "interoperability_verified": false})
}
