package deviceproof

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"wechat-bot/internal/sae08"
	"wechat-bot/internal/v08"
)

type runningVector struct {
	Seconds       int64
	Nanos         int64
	SecondSeconds int64 `json:"second_seconds"`
	SecondNanos   int64 `json:"second_nanos"`
	Deltas        []uint32
	Millis        int64
	WCSTF         string
	WCSTE         string
}

func runningFixtures(t *testing.T) []runningVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/running-static.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Vectors []runningVector }
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Vectors) != 6 {
		t.Fatal("incomplete fixture")
	}
	return fixture.Vectors
}

func TestRunningPythonProtobufFixtures(t *testing.T) {
	for index, vector := range runningFixtures(t) {
		now := time.Unix(vector.Seconds, vector.Nanos)
		later := time.Unix(vector.SecondSeconds, vector.SecondNanos)
		if actual := RunningMillis(now); actual != vector.Millis {
			t.Fatalf("clock %d: %d != %d", index, actual, vector.Millis)
		}
		inner, err := MarshalWCSTF(now, uint32(len(vector.Deltas)), vector.Deltas)
		if err != nil {
			t.Fatal(err)
		}
		if actual := hex.EncodeToString(inner); actual != vector.WCSTF {
			t.Fatalf("WCSTF %d: %s != %s", index, actual, vector.WCSTF)
		}
		if actual := hex.EncodeToString(MarshalWCSTE(now, later)); actual != vector.WCSTE {
			t.Fatalf("WCSTE %d: %s != %s", index, actual, vector.WCSTE)
		}
	}
}

func TestRunningClockSemantics(t *testing.T) {
	// The literal signed division truncates toward zero before the uint32 cast.
	beforeEpoch := time.Unix(-1, 999999999)
	if RunningMillis(beforeEpoch) != 0 || beforeEpoch.UnixMilli() != -1 {
		t.Fatal("UnixMilli incorrectly substituted for signed nanosecond division")
	}
	if actual := RunningMillis(time.Unix(0, -1000001)); actual != -1 {
		t.Fatalf("negative truncation: %d", actual)
	}
	current := time.Now() // includes a monotonic component on supported platforms
	if RunningMillis(current) != current.UnixMilli() {
		t.Fatal("monotonic time conversion mismatch")
	}
	// Retain the exact multiplication's modulo-64 overflow outside UnixNano's
	// defined date range rather than claiming wide-range UnixMilli semantics.
	farFuture := time.Unix(16725225600, 0) // 2500-01-01
	if got := RunningMillis(farFuture); got != -1721518473709 {
		t.Fatalf("overflow reconstruction: %d", got)
	}
}

func runningCipher(t *testing.T) *sae08.Cipher {
	t.Helper()
	raw, err := os.ReadFile("../../research/samples/08sae.dat.bin")
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := sae08.ParsePinned(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

type runningField struct {
	Number uint64
	Value  uint64
	Bytes  []byte
	Wire   uint64
}

// Independent bounded wire parser for assertions. It does not call builders.
func runningDecode(t *testing.T, data []byte) []runningField {
	t.Helper()
	var fields []runningField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 || tag>>3 == 0 {
			t.Fatal("invalid key")
		}
		data = data[n:]
		field := runningField{Number: tag >> 3, Wire: tag & 7}
		x, n := binary.Uvarint(data)
		if n <= 0 {
			t.Fatal("invalid value/length")
		}
		data = data[n:]
		switch field.Wire {
		case 0:
			field.Value = x
		case 2:
			if x > uint64(len(data)) {
				t.Fatal("invalid length")
			}
			field.Bytes = data[:int(x)]
			data = data[int(x):]
		default:
			t.Fatal("unsupported wire type")
		}
		fields = append(fields, field)
		if len(fields) > 8192 {
			t.Fatal("excess fields")
		}
	}
	return fields
}

func TestRunningCompleteWrapper(t *testing.T) {
	cipher := runningCipher(t)
	for index, vector := range runningFixtures(t) {
		now := time.Unix(vector.Seconds, vector.Nanos)
		later := time.Unix(vector.SecondSeconds, vector.SecondNanos)
		wcstf, err := BuildWCSTF(now, uint32(len(vector.Deltas)), vector.Deltas, cipher)
		if err != nil {
			t.Fatal(err)
		}
		wcste, err := BuildWCSTEAt(now, later, cipher)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			Encoded   []byte
			Inner     string
			Timestamp uint32
		}{{wcstf, vector.WCSTF, uint32(vector.Millis)}, {wcste, vector.WCSTE, uint32(vector.Seconds)}} {
			fields := runningDecode(t, item.Encoded)
			if len(fields) != 6 {
				t.Fatalf("wrapper %d missing required zero or other field: %+v", index, fields)
			}
			for i, field := range fields {
				if field.Number != uint64(i+1) {
					t.Fatal("wrapper tag order")
				}
			}
			if !bytes.Equal(fields[0].Bytes, []byte("00000008")) || fields[1].Value != 1 || fields[3].Value != uint64(item.Timestamp) || fields[4].Value != 5 || fields[5].Value != 0 {
				t.Fatalf("wrapper %d wrong metadata: %+v", index, fields)
			}
			inner, err := hex.DecodeString(item.Inner)
			if err != nil {
				t.Fatal(err)
			}
			compressed, err := v08.Compress(inner)
			if err != nil {
				t.Fatal(err)
			}
			want, err := cipher.EncodeZipData(compressed)
			if err != nil {
				t.Fatal(err)
			}
			if fields[2].Wire != 2 || !bytes.Equal(fields[2].Bytes, want) {
				t.Fatal("wrapper encrypted payload is not compression then EncodeZipData of Python protobuf fixture")
			}
		}
		convenience, err := BuildWCSTE(now, cipher)
		if err != nil {
			t.Fatal(err)
		}
		explicit, err := BuildWCSTEAt(now, now, cipher)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(convenience, explicit) {
			t.Fatal("one-time WCSTE convenience mismatch")
		}
	}
}

func TestRunningRejectsInvalidInputs(t *testing.T) {
	for _, item := range []struct {
		Count  uint32
		Deltas []uint32
	}{{1, nil}, {0, []uint32{1}}, {1, []uint32{10000}}, {MaxRunningCount + 1, nil}} {
		if out, err := MarshalWCSTF(time.Unix(0, 0), item.Count, item.Deltas); !errors.Is(err, ErrRunningInput) || out != nil {
			t.Fatalf("invalid input produced %x, %v", out, err)
		}
	}
	if _, err := BuildRunningInfo([]byte{8, 0}, 0, nil); !errors.Is(err, sae08.ErrTables) {
		t.Fatalf("nil cipher: %v", err)
	}
	if _, err := BuildRunningInfo(nil, 0, nil); !errors.Is(err, ErrRunningInput) {
		t.Fatalf("empty input: %v", err)
	}
	if _, err := BuildRunningInfo(make([]byte, sae08.MaxPlaintext+1), 0, nil); !errors.Is(err, ErrRunningInput) {
		t.Fatalf("oversize input: %v", err)
	}
}
