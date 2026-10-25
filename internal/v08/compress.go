package v08

import (
	"bytes"
	"compress/zlib"
	"errors"
)

// ErrEmptyInput replaces the recovered wrapper's invalid &input[0] operation
// for empty input with an explicit error. Device-data callers supply a message.
var ErrEmptyInput = errors.New("v08: cannot compress empty input")

// Compress produces an RFC 1950 zlib stream with the standard library's default
// compression level. The sample calls lib/libz.so's compressBound and compress
// (the latter selects zlib's default level). This provides the same stream
// format and decompressed content; DEFLATE bytes may differ between encoders.
func Compress(input []byte) ([]byte, error) {
	if len(input) == 0 {
		return nil, ErrEmptyInput
	}
	var output bytes.Buffer
	writer := zlib.NewWriter(&output)
	if _, err := writer.Write(input); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
