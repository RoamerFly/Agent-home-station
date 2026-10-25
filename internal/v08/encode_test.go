package v08

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestStaticInstructionVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/static-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Strings []struct {
			Input, Output       string
			Constant, Timestamp uint32
		}
		Integers []struct {
			Input, Output       string
			Constant, Timestamp uint32
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Strings) != 103 || len(fixture.Integers) != 28 {
		t.Fatal("incomplete static fixture")
	}
	for i, vector := range fixture.Strings {
		input, err := hex.DecodeString(vector.Input)
		if err != nil {
			t.Fatal(err)
		}
		before := bytes.Clone(input)
		encoded := EncodeString(input, vector.Constant, vector.Timestamp)
		if actual := hex.EncodeToString(encoded); actual != vector.Output {
			t.Fatalf("string vector %d: got %s want %s", i, actual, vector.Output)
		}
		if !bytes.Equal(input, before) {
			t.Fatalf("string vector %d changed input", i)
		}
	}
	for i, vector := range fixture.Integers {
		input, err := strconv.ParseUint(vector.Input, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		output, err := strconv.ParseUint(vector.Output, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if actual := EncodeUInt64(input, vector.Constant, vector.Timestamp); actual != output {
			t.Fatalf("integer vector %d: got %x want %x", i, actual, output)
		}
		var raw [8]byte
		binary.LittleEndian.PutUint64(raw[:], input)
		if actual := binary.LittleEndian.Uint64(EncodeString(raw[:], vector.Constant, vector.Timestamp)); actual != output {
			t.Fatalf("integer vector %d differs from explicit-length byte encoding", i)
		}
	}
}

func TestManualRotationCases(t *testing.T) {
	// One low word bit advances two positions; a high word bit advances four.
	if got := EncodeUInt64(0x0000000100000001, 0, 0); got != 0x0000001000000004 {
		t.Fatalf("rotation: %x", got)
	}
	// Distinct constants expose accidental reversal in the Go-to-C wrapper.
	if got := EncodeUInt64(0, 1, 0); got != 0x0000000800000002 {
		t.Fatalf("constant placement: %x", got)
	}
	if got := EncodeUInt64(0, 0, 1); got != 0x0000000200000002 {
		t.Fatalf("timestamp placement: %x", got)
	}
	if got := EncodeString([]byte{0x80, 0x40, 0x20}, 0, 0); !bytes.Equal(got, []byte{0x02, 0x04, 0x08}) {
		t.Fatalf("byte rotation: %x", got)
	}
}
