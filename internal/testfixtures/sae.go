// Package testfixtures provides deterministic, synthetic offline test inputs.
// These tables are not extracted artifacts and are not valid login credentials.
package testfixtures

import "encoding/binary"

// SAETables builds only the public envelope shape required by wrapper tests.
// Cryptographic correctness is tested separately against standard-library AES.
func SAETables() []byte {
	var out []byte
	field := func(tag uint64, data []byte) {
		out = binary.AppendUvarint(out, tag<<3|2)
		out = binary.AppendUvarint(out, uint64(len(data)))
		out = append(out, data...)
	}
	field(1, []byte("00000008"))
	field(2, []byte("synthetic-iv-000"))
	out = binary.AppendUvarint(out, 3<<3)
	out = binary.AppendUvarint(out, 0x3060)
	for _, entry := range []struct{ tag, size int }{{9, 128}, {10, 147456}, {11, 64}, {12, 110592}, {18, 4096}} {
		data := make([]byte, entry.size)
		for i := range data {
			data[i] = byte(i*37 + entry.tag)
		}
		field(uint64(entry.tag), data)
	}
	return out
}
