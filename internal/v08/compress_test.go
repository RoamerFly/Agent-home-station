package v08

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/adler32"
	"io"
	"testing"
)

func TestCompressZlibFormatAndContents(t *testing.T) {
	for _, input := range [][]byte{[]byte("hello"), {0}, {0, 255, 0, 128, 1}, bytes.Repeat([]byte("device-data\x00"), 4096)} {
		before := bytes.Clone(input)
		output, err := Compress(input)
		if err != nil {
			t.Fatal(err)
		}
		if len(output) < 6 || output[0]&15 != 8 || (uint16(output[0])<<8|uint16(output[1]))%31 != 0 || output[1]&0x20 != 0 {
			t.Fatalf("invalid RFC 1950 header: %x", output)
		}
		if got := binary.BigEndian.Uint32(output[len(output)-4:]); got != adler32.Checksum(input) {
			t.Fatal("wrong Adler-32 trailer")
		}
		reader, err := zlib.NewReader(bytes.NewReader(output))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, input) || !bytes.Equal(input, before) {
			t.Fatal("content mismatch or input changed")
		}
	}
}

func TestCompressEmptyInput(t *testing.T) {
	for _, input := range [][]byte{nil, {}} {
		output, err := Compress(input)
		if !errors.Is(err, ErrEmptyInput) || output != nil {
			t.Fatalf("empty: %x %v", output, err)
		}
	}
}
