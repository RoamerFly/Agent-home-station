// Package sae08 reconstructs the version-8 EncodeZipData table transform.
// It contains no original executable code. The caller supplies opaque table
// data separately; recovering the table-generation algorithm is out of scope.
package sae08

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const (
	BlockSize    = 16
	MaxPlaintext = 2 << 20
	SampleSHA256 = "dfedf50d53e3579691bf428fa9686883ffe91b9781fd587e08e9808617a152a3"
)

var ErrTables = errors.New("sae08: invalid or unsupported table data")
var ErrPlaintext = errors.New("sae08: invalid plaintext length")

// Cipher is immutable after Parse and safe for concurrent use. Each EncodeZipData
// invocation starts with the table's IV; chaining is local to that invocation.
type Cipher struct {
	iv    [16]byte
	key   []byte
	value []byte
	final []byte
}

// ParsePinned accepts only the frozen, statically extracted 08sae.dat sample.
// Parse can instead be used when a caller independently verifies other data.
func ParsePinned(data []byte) (*Cipher, error) {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != SampleSHA256 {
		return nil, ErrTables
	}
	return Parse(data)
}

// Parse validates the version-8 protobuf table envelope and reconstructs the
// two masked tables. Shape validation does not authenticate a table's origin.
func Parse(data []byte) (*Cipher, error) {
	if len(data) == 0 || len(data) > 512<<10 {
		return nil, ErrTables
	}
	fields := make(map[uint64][]byte)
	seen := make(map[uint64]bool)
	var mode uint64
	for len(data) > 0 {
		tag, n := varint(data)
		if n == 0 {
			return nil, ErrTables
		}
		data = data[n:]
		field, wire := tag>>3, tag&7
		if field == 0 || field > (1<<29)-1 || seen[field] {
			return nil, ErrTables
		}
		seen[field] = true
		switch wire {
		case 0:
			v, n := varint(data)
			if n == 0 {
				return nil, ErrTables
			}
			data = data[n:]
			if field == 3 {
				mode = v
			} else if field != 50 {
				return nil, ErrTables
			}
		case 2:
			length, n := varint(data)
			if n == 0 || length > uint64(len(data)-n) {
				return nil, ErrTables
			}
			data = data[n:]
			fields[field] = data[:int(length)]
			data = data[int(length):]
		default:
			return nil, ErrTables
		}
	}
	if mode != 0x3060 || string(fields[1]) != "00000008" || len(fields[2]) != 16 || len(fields[9]) != 128 || len(fields[10]) != 147456 || len(fields[11]) != 64 || len(fields[12]) != 110592 || len(fields[18]) != 4096 {
		return nil, ErrTables
	}
	for k := range fields {
		if k != 1 && k != 2 && k != 9 && k != 10 && k != 11 && k != 12 && k != 18 {
			return nil, ErrTables
		}
	}
	c := &Cipher{key: append([]byte(nil), fields[10]...), value: append([]byte(nil), fields[12]...), final: append([]byte(nil), fields[18]...)}
	copy(c.iv[:], fields[2])
	for r := 0; r < 9; r++ {
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				for x := 0; x < 256; x++ {
					for component := 0; component < 4; component++ {
						index := r*0x4000 + row*0x1000 + col*0x400 + x*4 + component
						c.key[index] ^= fields[9][(r*row*col+x+component+7)&127]
					}
				}
			}
		}
	}
	for r := 0; r < 9; r++ {
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				for stage := 0; stage < 3; stage++ {
					for half := 0; half < 2; half++ {
						for x := 0; x < 128; x++ {
							index := r*0x3000 + row*0xc00 + col*0x300 + stage*0x100 + half*0x80 + x
							c.value[index] ^= fields[11][(r*row*col+stage*half+x+23)&63]
						}
					}
				}
			}
		}
	}
	return c, nil
}

func varint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		if i == 9 && b[i] > 1 {
			return 0, 0
		}
		x |= uint64(b[i]&127) << (7 * i)
		if b[i] < 128 {
			return x, i + 1
		}
	}
	return 0, 0
}

// EncodeZipData performs only the recovered byte transform, not zlib compression.
// A non-aligned input receives PKCS#7-style bytes up to the next block. An
// already aligned input receives no extra block, matching the selected binary.
func (c *Cipher) EncodeZipData(plain []byte) ([]byte, error) {
	return c.encode(plain, false)
}

// EncryptZT reconstructs Algorithm.(*ZT).Encrypt. It uses the same table
// transform with full PKCS#7 padding, including one whole block for aligned or
// empty input. Plaintext is limited to MaxPlaintext bytes.
func (c *Cipher) EncryptZT(plain []byte) ([]byte, error) {
	return c.encode(plain, true)
}

func (c *Cipher) encode(plain []byte, fullPadding bool) ([]byte, error) {
	if c == nil || len(c.key) != 147456 || len(c.value) != 110592 || len(c.final) != 4096 {
		return nil, ErrTables
	}
	if (len(plain) == 0 && !fullPadding) || len(plain) > MaxPlaintext {
		return nil, ErrPlaintext
	}
	n := (len(plain) + 15) &^ 15
	if fullPadding {
		n = (len(plain)/16 + 1) * 16
	}
	input := make([]byte, n)
	copy(input, plain)
	for i := len(plain); i < n; i++ {
		input[i] = byte(n - len(plain))
	}
	out := make([]byte, n)
	previous := c.iv
	for offset := 0; offset < n; offset += 16 {
		var state [16]byte
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				state[row*4+col] = input[offset+col*4+row] ^ previous[col*4+row]
			}
		}
		shiftRows(&state)
		for round := 0; round < 9; round++ {
			var expanded [64]byte
			for row := 0; row < 4; row++ {
				for col := 0; col < 4; col++ {
					base := round*0x4000 + (row*4+col)*0x400 + int(state[row*4+col])*4
					for component := 0; component < 4; component++ {
						expanded[component*16+col*4+row] = c.key[base+component]
					}
				}
			}
			for row := 0; row < 4; row++ {
				for col := 0; col < 4; col++ {
					pos := row*4 + col
					acc := expanded[row*16+col*4+3]
					for step := 0; step < 3; step++ {
						b := expanded[row*16+col*4+2-step]
						base := round*0x3000 + pos*0x300 + (2-step)*0x100
						hi, lo := (b&0xf0)|(acc>>4), (b<<4)|(acc&15)
						acc = nibble(c.value[base+int(hi&127)], hi)<<4 | nibble(c.value[base+128+int(lo&127)], lo)
					}
					state[pos] = acc
				}
			}
			shiftRows(&state)
		}
		for row := 0; row < 4; row++ {
			for col := 0; col < 4; col++ {
				previous[col*4+row] = c.final[(row*4+col)*256+int(state[row*4+col])]
			}
		}
		copy(out[offset:offset+16], previous[:])
	}
	return out, nil
}

func nibble(packed, index byte) byte {
	if index&128 != 0 {
		return packed >> 4
	}
	return packed & 15
}
func shiftRows(state *[16]byte) {
	old := *state
	for row := 1; row < 4; row++ {
		for col := 0; col < 4; col++ {
			state[row*4+col] = old[row*4+(col+row)%4]
		}
	}
}
