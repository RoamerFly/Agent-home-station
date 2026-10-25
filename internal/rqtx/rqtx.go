// Package rqtx implements the bounded, data-driven rqtx VM recovered from a
// pinned libv08 sample. The embedded program is opaque bytecode/data, not a
// recovered mathematical description of the protocol algorithm.
package rqtx

import (
	"crypto/md5"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	programSHA256 = "57def9708a23be4992fc0024754da9eaa8179651d210a3fe5e72cf3d9cdd2c6d"
	maxSteps      = 2_000_000

	codeBase   uint64 = 0x100000
	stateBase  uint64 = 0x200000
	inputBase  uint64 = 0x400000
	resultBase uint64 = 0x500000
	stackBase  uint64 = 0x700000
)

var (
	//go:embed program.hex
	programHex string

	programOnce sync.Once
	programData []byte
	programErr  error
)

// SumHex evaluates the embedded rqtx program for exactly 32 lowercase
// hexadecimal characters. It makes a fresh VM state for every invocation.
func SumHex(input string) (uint32, error) {
	if len(input) != 32 || !isLowerHex(input) {
		return 0, errors.New("rqtx: input must be 32 lowercase hexadecimal characters")
	}
	data, err := loadProgram()
	if err != nil {
		return 0, err
	}
	return calculate(data, []byte(input))
}

// SignBody takes the lowercase MD5 digest of body and evaluates it with SumHex.
func SignBody(body []byte) (uint32, error) {
	digest := md5.Sum(body)
	return SumHex(hex.EncodeToString(digest[:]))
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func loadProgram() ([]byte, error) {
	programOnce.Do(func() {
		encoded := strings.TrimSpace(programHex)
		programData, programErr = hex.DecodeString(encoded)
		if programErr != nil {
			programErr = fmt.Errorf("rqtx: decode embedded program: %w", programErr)
			return
		}
		if len(programData) < 512 {
			programErr = errors.New("rqtx: embedded program is too short")
			return
		}
		digest := sha256.Sum256([]byte(encoded))
		if hex.EncodeToString(digest[:]) != programSHA256 {
			programErr = errors.New("rqtx: embedded program checksum mismatch")
		}
	})
	return programData, programErr
}

type vm struct {
	table  []byte
	code   []byte
	state  []byte
	input  []byte
	result []byte
	stack  []byte
}

func calculate(raw, input []byte) (uint32, error) {
	v := vm{
		table:  raw[:512],
		code:   make([]byte, 0x24000),
		state:  make([]byte, 0x80440),
		input:  append([]byte(nil), input...),
		result: make([]byte, 16),
		stack:  make([]byte, 4),
	}
	copy(v.code, raw[512:])
	binary.LittleEndian.PutUint32(v.stack, 0x54000000)

	for _, move := range [][2]uint64{{0x1c000, 0x288}, {0x1c0b8, 0x210}, {0x1c0c0, 0x228}, {0x1c0c8, 0x240}, {0x1c0d0, 0x258}, {0x1c0d8, 0x270}} {
		x, err := v.read(codeBase+move[1], 8)
		if err != nil {
			return 0, err
		}
		if err := v.write(codeBase+move[0], codeBase+x, 8); err != nil {
			return 0, err
		}
	}
	for i, value := range []uint64{inputBase, 32, resultBase, resultBase + 8} {
		if err := v.put(uint64(i), value); err != nil {
			return 0, err
		}
	}
	if err := v.put(125, 0x280400); err != nil {
		return 0, err
	}
	if err := v.put(127, stackBase); err != nil {
		return 0, err
	}
	if err := v.write(resultBase+8, 4, 8); err != nil {
		return 0, err
	}

	pc := codeBase + 0x4000
	var a, b, c uint64
	var imm12, imm19, offset26 int64
	var raw12, raw19 uint64
	for step := 0; step < maxSteps; step++ {
		ip := pc
		ins, err := v.read(ip, 4)
		if err != nil {
			return 0, v.stepError(step, ip, 0, 0, err)
		}
		pc = ip + 4
		op := ins >> 26
		if ip != stackBase {
			op ^= (ip >> 8) & 63
			index := op * 2
			if index+1 >= uint64(len(v.table)) {
				return 0, v.stepError(step, ip, ins, op, errors.New("opcode table index out of bounds"))
			}
			if v.table[index] != 0 {
				op = uint64(v.table[index+1])
			}
		}

		group := byte(0)
		if op < 15 {
			if op == 0 {
				return 0, v.stepError(step, ip, ins, op, errors.New("VM abort"))
			}
			if (uint64(1)<<op)&0x15b4 != 0 {
				group = 'R'
			} else if (uint64(1)<<op)&0x2a4a != 0 {
				group = 'I'
			} else if (uint64(1)<<op)&0x4000 != 0 {
				group = 'U'
			}
		} else if op < 28 {
			switch op {
			case 15, 16, 17, 22, 23, 24:
				group = 'I'
			case 18, 19, 20, 21, 25, 26:
				group = 'R'
			case 27:
				group = 'J'
			}
		} else if op < 56 {
			if (uint64(1)<<op)&0x7c2abd10000000 != 0 {
				group = 'R'
			} else if (uint64(1)<<op)&0x834142a0000000 != 0 {
				group = 'I'
			} else if (uint64(1)<<op)&0x940040000000 != 0 {
				group = 'U'
			}
		}
		if group == 'R' || group == 'I' || group == 'U' {
			a = ((ins >> 19) & 127) ^ ((ip >> 2) & 127)
		}
		if group == 'R' || group == 'I' {
			b = ((ins >> 12) ^ (ins >> 19)) & 127
		}
		if group == 'R' {
			c = ((ins >> 5) ^ (ins >> 12)) & 127
		}
		if group == 'I' {
			raw12 = ins & 4095
			imm12 = signExtend(raw12, 12)
		}
		if group == 'U' {
			raw19 = ins & 0x7ffff
			imm19 = signExtend(raw19, 19)
		}
		if group == 'J' {
			offset26 = signExtend(ins&0x3ffffff, 26)
		}

		if err := v.execute(op, &pc, ip, a, b, c, imm12, imm19, raw12, raw19, offset26); err != nil {
			return 0, v.stepError(step, ip, ins, op, err)
		}
		if op == 21 {
			out, err := v.read(resultBase, 4)
			return uint32(out), err
		}
	}
	return 0, errors.New("rqtx: VM instruction limit")
}

func signExtend(value uint64, bits uint) int64 { return int64(value<<(64-bits)) >> (64 - bits) }

func (v *vm) execute(op uint64, pc *uint64, ip, a, b, c uint64, imm12, imm19 int64, raw12, raw19 uint64, offset26 int64) error {
	reg := func(i uint64) (uint64, error) { return v.get(i) }
	addr := func(base uint64) uint64 { return base + uint64(imm12) }
	switch op {
	case 1, 2:
		x, e := reg(127)
		if e != nil {
			return e
		}
		*pc = x
	case 3:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x+uint64(imm12))
	case 4, 5, 19:
		x, e := reg(c)
		if e != nil {
			return e
		}
		y, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, boolWord(int64(x) < int64(y)))
	case 6:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x&(uint64(imm12)))
	case 7, 8:
		x, e := reg(c)
		if e != nil {
			return e
		}
		y, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x^y)
	case 9:
		x, e := reg(b)
		if e != nil {
			return e
		}
		if raw12 == 0 {
			return errors.New("division by zero")
		}
		return v.put(a, x%raw12)
	case 10, 11:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x*uint64(imm12))
	case 12, 13:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := reg(a)
		if e != nil {
			return e
		}
		return v.write(addr(x), value, 8)
	case 14:
		return v.put(a, uint64(imm19))
	case 15, 16:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x^uint64(imm12))
	case 17:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x-uint64(imm12))
	case 18:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, x-y)
	case 20:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, boolWord(x != y))
	case 21:
		return nil
	case 22:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := v.read(addr(x), 4)
		if e != nil {
			return e
		}
		return v.put(a, uint64(int64(int32(value))))
	case 23:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := reg(a)
		if e != nil {
			return e
		}
		return v.write(addr(x), value, 2)
	case 24, 25:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, uint64(int64(x)>>(uint64(imm12)&63)))
	case 26:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, boolWord(x == y))
	case 27:
		*pc = uint64(int64(ip) + offset26)
	case 28, 29, 30:
		x, e := reg(a)
		if e != nil {
			return e
		}
		return v.put(a, (x&^0x7ffff)^raw19)
	case 31, 32:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x<<(uint64(imm12)&63))
	case 33, 34:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := v.read(addr(x), 1)
		if e != nil {
			return e
		}
		return v.put(a, uint64(int64(int8(value))))
	case 35, 36:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, x&y)
	case 37:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, x|y)
	case 38:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := v.read(addr(x), 8)
		if e != nil {
			return e
		}
		return v.put(a, value)
	case 39:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, boolWord(int64(x) < int64(y)))
	case 40, 41:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := reg(a)
		if e != nil {
			return e
		}
		return v.write(addr(x), value, 4)
	case 42:
		x, e := reg(a)
		if e != nil {
			return e
		}
		if x == 1 {
			*pc = uint64(int64(ip) + imm19)
		}
	case 43, 44:
		return v.put(a, uint64(imm19*0x4000+int64(ip&^0x3fff)))
	case 45:
		x, e := reg(c)
		if e != nil {
			return e
		}
		if x == 1 {
			y, e := reg(b)
			if e != nil {
				return e
			}
			return v.put(a, y)
		}
	case 46:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := v.read(addr(x), 2)
		if e != nil {
			return e
		}
		return v.put(a, uint64(int64(int16(value))))
	case 47:
		x, e := reg(a)
		if e != nil {
			return e
		}
		return v.put(a, x+(raw19&0x3fff))
	case 48:
		x, e := reg(b)
		if e != nil {
			return e
		}
		value, e := reg(a)
		if e != nil {
			return e
		}
		return v.write(addr(x), value, 1)
	case 49:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x|uint64(imm12))
	case 50, 51:
		target, e := reg(a)
		if e != nil {
			return e
		}
		if e = v.put(127, *pc); e != nil {
			return e
		}
		*pc = target
	case 52:
		x, e := reg(b)
		if e != nil {
			return e
		}
		return v.put(a, x)
	case 53, 54, 55:
		x, e := reg(b)
		if e != nil {
			return e
		}
		y, e := reg(c)
		if e != nil {
			return e
		}
		return v.put(a, x+y)
	default:
		return fmt.Errorf("unknown opcode %d", op)
	}
	return nil
}

func boolWord(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}
func (v *vm) get(index uint64) (uint64, error) { return v.read(stateBase+index*8, 8) }
func (v *vm) put(index, value uint64) error {
	if index >= 128 {
		return errors.New("register index out of bounds")
	}
	return v.write(stateBase+index*8, value, 8)
}
func (v *vm) read(address uint64, size int) (uint64, error) {
	segment, offset, err := v.locate(address, size)
	if err != nil {
		return 0, err
	}
	switch size {
	case 1:
		return uint64(segment[offset]), nil
	case 2:
		return uint64(binary.LittleEndian.Uint16(segment[offset:])), nil
	case 4:
		return uint64(binary.LittleEndian.Uint32(segment[offset:])), nil
	case 8:
		return binary.LittleEndian.Uint64(segment[offset:]), nil
	}
	return 0, errors.New("unsupported read width")
}
func (v *vm) write(address, value uint64, size int) error {
	segment, offset, err := v.locate(address, size)
	if err != nil {
		return err
	}
	switch size {
	case 1:
		segment[offset] = byte(value)
	case 2:
		binary.LittleEndian.PutUint16(segment[offset:], uint16(value))
	case 4:
		binary.LittleEndian.PutUint32(segment[offset:], uint32(value))
	case 8:
		binary.LittleEndian.PutUint64(segment[offset:], value)
	default:
		return errors.New("unsupported write width")
	}
	return nil
}
func (v *vm) locate(address uint64, size int) ([]byte, int, error) {
	if size < 0 {
		return nil, 0, errors.New("negative memory width")
	}
	for _, segment := range []struct {
		base uint64
		data []byte
	}{{codeBase, v.code}, {stateBase, v.state}, {inputBase, v.input}, {resultBase, v.result}, {stackBase, v.stack}} {
		if address >= segment.base {
			offset := address - segment.base
			if offset <= uint64(len(segment.data)) && uint64(size) <= uint64(len(segment.data))-offset {
				return segment.data, int(offset), nil
			}
		}
	}
	return nil, 0, fmt.Errorf("unmapped VM address %#x+%d", address, size)
}
func (v *vm) stepError(step int, ip, ins, op uint64, cause error) error {
	return fmt.Errorf("rqtx: step=%d ip=%#x ins=%08x op=%d: %w", step, ip, ins, op, cause)
}
