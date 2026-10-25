// Package v08 reconstructs the small encoding and compression primitives used by
// the fixed sample's device-data builder. It never loads the original library.
// These encodings are reversible obfuscation, not authenticated encryption.
package v08

import (
	"encoding/binary"
	"math/bits"
)

// EncodeString reproduces the explicit-length encode_cstr wrapper. Embedded NUL
// bytes are data. The input is not modified and the output has the same length.
// constant and timestamp retain the argument order of the sample's Go wrapper.
func EncodeString(input []byte, constant, timestamp uint32) []byte {
	output := make([]byte, len(input))
	groups := len(input) / 4
	for i := 0; i < groups; i++ {
		x := binary.LittleEndian.Uint32(input[i*4:])
		x = bits.RotateLeft32(x, i+1)
		x ^= bits.RotateLeft32(timestamp, -i) ^ bits.RotateLeft32(constant, i)
		x = bits.RotateLeft32(x, i+1)
		binary.LittleEndian.PutUint32(output[i*4:], x)
	}
	// The native tail helper restarts its index at zero and receives only the
	// low byte of constant. Only timestamp's low byte survives the output cast.
	for i, x := range input[groups*4:] {
		x = bits.RotateLeft8(x, i+1)
		x ^= bits.RotateLeft8(byte(timestamp), -i) ^ bits.RotateLeft8(byte(constant), i)
		output[groups*4+i] = bits.RotateLeft8(x, i+1)
	}
	return output
}

// EncodeUInt64 reproduces the sample's Go EncodeUInt64 argument order, including
// its reversal of the two native scalar parameters. The native function encodes
// the low and high 32-bit words separately in little-endian order.
func EncodeUInt64(input uint64, constant, timestamp uint32) uint64 {
	low := bits.RotateLeft32(uint32(input), 1)
	low = bits.RotateLeft32(low^timestamp^constant, 1)
	high := bits.RotateLeft32(uint32(input>>32), 2)
	high ^= bits.RotateLeft32(timestamp, -1) ^ bits.RotateLeft32(constant, 1)
	high = bits.RotateLeft32(high, 2)
	return uint64(low) | uint64(high)<<32
}
