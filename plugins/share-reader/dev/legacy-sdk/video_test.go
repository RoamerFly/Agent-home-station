package channels

import (
	"bytes"
	"testing"
)

func TestShortInputAndPrefixBoundary(t *testing.T) {
	for _, size := range []int{0, 1, 7, 8, 9, 1024, 128 << 10, (128 << 10) + 128} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i*17 + 9)
		}
		before := bytes.Clone(data)
		DecodePrefix(data, 123456789)
		if size > 0 && bytes.Equal(data, before) {
			t.Fatal("prefix unchanged", size)
		}
		if size > 128<<10 && !bytes.Equal(data[128<<10:], before[128<<10:]) {
			t.Fatal("plaintext tail changed")
		}
		DecodePrefix(data, 123456789)
		if !bytes.Equal(data, before) {
			t.Fatal("round trip", size)
		}
	}
}
