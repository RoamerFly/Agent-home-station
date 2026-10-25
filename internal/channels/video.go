// Package channels handles the prefix transform for video bytes whose decode
// key was supplied by the same received WeChat Channels card.
package channels

import "encoding/binary"

// DecodePrefix modifies only the first 128 KiB, including short inputs. It does
// not authenticate the result: callers must validate it as playable media.
func DecodePrefix(data []byte, key uint64) {
	stream := CreateISAacInst(key)
	limit := min(len(data), 128<<10)
	var block [8]byte
	for offset := 0; offset < limit; offset += 8 {
		binary.BigEndian.PutUint64(block[:], stream.ISAacRandom())
		for i := 0; i < 8 && offset+i < limit; i++ {
			data[offset+i] ^= block[i]
		}
	}
}
