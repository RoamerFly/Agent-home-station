package wechatwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
)

func fixture(t *testing.T) (Text, string) {
	t.Helper()
	b, err := os.ReadFile("testdata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Recipient string `json:"recipient"`
		Content   string `json:"content"`
		ClientID  string `json:"client_message_id"`
		UnixTime  int64  `json:"unix_time"`
		ServerID  string `json:"server_message_id"`
	}
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseUint(m.ClientID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return Text{Recipient: m.Recipient, Content: m.Content, ClientMessageID: id, UnixTime: m.UnixTime}, m.ServerID
}

// This compares against a different protobuf runtime using the artifact's schema.
// It does not claim that either side contacted or received an acknowledgement from WeChat.
func TestRecoveredSchemaGolden(t *testing.T) {
	req, wantID := fixture(t)
	got, err := MarshalText(req)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/request.pb")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wire mismatch\n got %x\nwant %x", got, want)
	}
	b, err := os.ReadFile("testdata/ack.pb")
	if err != nil {
		t.Fatal(err)
	}
	ack, err := ParseAcknowledgement(b)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ack.Validate(req)
	if err != nil {
		t.Fatal(err)
	}
	if id != wantID {
		t.Fatalf("uint64 message ID corrupted: %s", id)
	}
}

func TestAcknowledgementMustCorrelateAndContainExplicitSuccess(t *testing.T) {
	req, _ := fixture(t)
	b, _ := os.ReadFile("testdata/ack.pb")
	valid, err := ParseAcknowledgement(b)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Acknowledgement){
		"missing-base":      func(a *Acknowledgement) { a.HasBaseResponse = false },
		"missing-base-ret":  func(a *Acknowledgement) { a.HasReturnCode = false },
		"base-rejected":     func(a *Acknowledgement) { a.ReturnCode = -1 },
		"missing-count":     func(a *Acknowledgement) { a.HasCount = false },
		"wrong-count":       func(a *Acknowledgement) { a.Count = 2 },
		"no-item":           func(a *Acknowledgement) { a.Items = nil },
		"missing-item-ret":  func(a *Acknowledgement) { a.Items[0].HasReturnCode = false },
		"item-rejected":     func(a *Acknowledgement) { a.Items[0].ReturnCode = -44 },
		"wrong-recipient":   func(a *Acknowledgement) { a.Items[0].Recipient = "other" },
		"wrong-client-id":   func(a *Acknowledgement) { a.Items[0].ClientMessageID++ },
		"missing-client-id": func(a *Acknowledgement) { a.Items[0].HasClientMessageID = false },
		"no-message-id":     func(a *Acknowledgement) { a.Items[0].MessageID = 0; a.Items[0].NewMessageID = 0 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			a := valid
			a.Items = append([]Item(nil), valid.Items...)
			change(&a)
			if _, err := a.Validate(req); err == nil {
				t.Fatal("accepted incomplete or mismatched acknowledgement")
			}
		})
	}
}

func TestMalformedWire(t *testing.T) {
	for _, b := range [][]byte{nil, {0}, {0x80}, {0x0a, 0xff, 0x01}, {0x08, 0}, {0x0a, 0x01, 0xff}, {0x0a, 0x02, 0x0a, 0x00}, bytes.Repeat([]byte{0}, MaxPayload+1)} {
		if _, err := ParseAcknowledgement(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("expected malformed: %x (%v)", b[:min(len(b), 16)], err)
		}
	}
}

func TestMissingRetInActualWireFailsClosed(t *testing.T) {
	req, _ := fixture(t)
	b := appendBytes(nil, 1, nil) // present BaseResponse, absent ret
	b = appendInt(b, 2, 1)
	ack, err := ParseAcknowledgement(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ack.Validate(req); err == nil {
		t.Fatal("absent ret treated as zero success")
	}
}

func TestUnknownFieldSkipped(t *testing.T) {
	req, _ := fixture(t)
	b, _ := os.ReadFile("testdata/ack.pb")
	b = appendBytes(b, 99, []byte("future"))
	ack, err := ParseAcknowledgement(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ack.Validate(req); err != nil {
		t.Fatal(err)
	}
}

func FuzzParseAcknowledgement(f *testing.F) {
	b, _ := os.ReadFile("testdata/ack.pb")
	f.Add(b)
	f.Add([]byte{0x80})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = ParseAcknowledgement(b) })
}
