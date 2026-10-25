// Package wechatwire implements the text-message protobuf subset recovered from
// the pinned artifact's embedded mm.proto descriptor. It contains no original code.
package wechatwire

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxPayload = 2 << 20

var ErrMalformed = errors.New("malformed message acknowledgement")

type Text struct {
	Recipient       string
	Content         string
	ClientMessageID uint64
	UnixTime        int64
	MessageSource   string
}

// NewText chooses a nonzero positive 63-bit ID. The server must echo the ID before
// an acknowledgement is accepted. This generator is a reconstruction choice,
// not a claim about the original binary's random-number generator.
func NewText(recipient, content string, now time.Time) (Text, error) {
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return Text{}, err
	}
	id := binary.LittleEndian.Uint64(seed[:]) & ((1 << 63) - 1)
	if id == 0 {
		id = 1
	}
	return Text{Recipient: recipient, Content: content, ClientMessageID: id, UnixTime: now.Unix()}, nil
}

// MarshalText encodes NewSendMsgRequest(cnt=1, info=ChatInfo(type=1)). The wire
// schema is recovered; MessageSource defaults and envelope behavior require the
// separate send-path analysis. This function never transmits bytes.
func MarshalText(t Text) ([]byte, error) {
	if t.Recipient == "" || strings.TrimSpace(t.Content) == "" || !utf8.ValidString(t.Recipient) || !utf8.ValidString(t.Content) || !utf8.ValidString(t.MessageSource) {
		return nil, errors.New("recipient and text must be nonempty valid UTF-8")
	}
	if t.ClientMessageID == 0 || t.UnixTime <= 0 {
		return nil, errors.New("message ID and timestamp must be positive")
	}
	if len(t.Content) > 4096 || len(t.Recipient) > 256 || len(t.MessageSource) > 4096 {
		return nil, errors.New("text exceeds local sender limits")
	}
	var recipient []byte
	recipient = appendBytes(recipient, 1, []byte(t.Recipient))
	var chat []byte
	chat = appendBytes(chat, 1, recipient)
	chat = appendBytes(chat, 2, []byte(t.Content))
	chat = appendInt(chat, 3, 1)
	chat = appendInt(chat, 4, uint64(t.UnixTime))
	chat = appendInt(chat, 5, t.ClientMessageID)
	if t.MessageSource != "" {
		chat = appendBytes(chat, 6, []byte(t.MessageSource))
	}
	out := appendInt(nil, 1, 1)
	return appendBytes(out, 2, chat), nil
}

type Item struct {
	ReturnCode         int64
	Recipient          string
	MessageID          uint64
	ClientMessageID    uint64
	NewMessageID       uint64
	HasReturnCode      bool
	HasClientMessageID bool
}

type Acknowledgement struct {
	ReturnCode      int32
	ErrorMessage    string
	Count           int32
	Items           []Item
	HasBaseResponse bool
	HasReturnCode   bool
	HasCount        bool
}

// ParseAcknowledgement retains presence separately: absent return codes must not
// be mistaken for successful zero return codes. Unknown fields are skipped.
func ParseAcknowledgement(b []byte) (Acknowledgement, error) {
	if len(b) == 0 || len(b) > MaxPayload {
		return Acknowledgement{}, ErrMalformed
	}
	var ack Acknowledgement
	err := fields(b, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 2 {
				return ErrMalformed
			}
			ack.HasBaseResponse = true
			return fields(f.bytes, func(base field) error {
				switch base.number {
				case 1:
					if base.wire != 0 {
						return ErrMalformed
					}
					ack.ReturnCode = int32(base.value)
					ack.HasReturnCode = true
				case 2:
					if base.wire != 2 {
						return ErrMalformed
					}
					s, err := readString(base.bytes)
					if err != nil {
						return err
					}
					ack.ErrorMessage = s
				}
				return nil
			})
		case 2:
			if f.wire != 0 {
				return ErrMalformed
			}
			ack.Count = int32(f.value)
			ack.HasCount = true
		case 3:
			if f.wire != 2 || len(ack.Items) >= 1024 {
				return ErrMalformed
			}
			item, err := parseItem(f.bytes)
			if err != nil {
				return err
			}
			ack.Items = append(ack.Items, item)
		}
		return nil
	})
	if err != nil {
		return Acknowledgement{}, err
	}
	return ack, nil
}

func parseItem(b []byte) (Item, error) {
	var item Item
	err := fields(b, func(f field) error {
		switch f.number {
		case 1:
			if f.wire != 0 {
				return ErrMalformed
			}
			item.ReturnCode = int64(f.value)
			item.HasReturnCode = true
		case 2:
			if f.wire != 2 {
				return ErrMalformed
			}
			s, err := readString(f.bytes)
			if err != nil {
				return err
			}
			item.Recipient = s
		case 3, 4, 8:
			if f.wire != 0 {
				return ErrMalformed
			}
			if f.number == 3 {
				item.MessageID = f.value
			}
			if f.number == 4 {
				item.ClientMessageID = f.value
				item.HasClientMessageID = true
			}
			if f.number == 8 {
				item.NewMessageID = f.value
			}
		}
		return nil
	})
	return item, err
}

// Validate checks the single-message response against the request. Success means
// an authenticated upstream accepted it, not that it was read or even delivered.
// Authentication/decryption must be done by the transport before calling this.
func (a Acknowledgement) Validate(t Text) (string, error) {
	if !a.HasBaseResponse || !a.HasReturnCode {
		return "", ErrMalformed
	}
	if a.ReturnCode != 0 {
		return "", fmt.Errorf("wechat base return code %d", a.ReturnCode)
	}
	if !a.HasCount || a.Count != 1 || len(a.Items) != 1 {
		return "", ErrMalformed
	}
	i := a.Items[0]
	if !i.HasReturnCode {
		return "", ErrMalformed
	}
	if i.ReturnCode != 0 {
		return "", fmt.Errorf("wechat message return code %d", i.ReturnCode)
	}
	if i.Recipient != t.Recipient || !i.HasClientMessageID || i.ClientMessageID != t.ClientMessageID {
		return "", ErrMalformed
	}
	id := i.NewMessageID
	if id == 0 {
		id = i.MessageID
	}
	if id == 0 {
		return "", ErrMalformed
	}
	return strconv.FormatUint(id, 10), nil
}

func appendInt(b []byte, n int, v uint64) []byte {
	b = binary.AppendUvarint(b, uint64(n<<3))
	return binary.AppendUvarint(b, v)
}
func appendBytes(b []byte, n int, v []byte) []byte {
	b = binary.AppendUvarint(b, uint64(n<<3|2))
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

type field struct {
	number int
	wire   uint64
	value  uint64
	bytes  []byte
}

func fields(b []byte, visit func(field) error) error {
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return ErrMalformed
		}
		b = b[n:]
		f := field{number: int(tag >> 3), wire: tag & 7}
		if f.number <= 0 || f.number > 536870911 {
			return ErrMalformed
		}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return ErrMalformed
			}
			f.value = v
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return ErrMalformed
			}
			b = b[8:]
		case 2:
			length, n := binary.Uvarint(b)
			if n <= 0 {
				return ErrMalformed
			}
			b = b[n:]
			if length > uint64(len(b)) {
				return ErrMalformed
			}
			f.bytes = b[:int(length)]
			b = b[int(length):]
		case 5:
			if len(b) < 4 {
				return ErrMalformed
			}
			b = b[4:]
		default:
			return ErrMalformed // Legacy groups are outside this recovered schema.
		}
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}
func readString(b []byte) (string, error) {
	var s string
	err := fields(b, func(f field) error {
		if f.number == 1 {
			if f.wire != 2 || !utf8.Valid(f.bytes) {
				return ErrMalformed
			}
			s = string(f.bytes)
		}
		return nil
	})
	return s, err
}
