// Package deviceproof reconstructs the fixed sample's device-data producers.
// Successful serialization does not establish server acceptance of that data.
package deviceproof

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"wechat-bot/internal/sae08"
	"wechat-bot/internal/v08"
)

const MaxRunningCount = 4096

var ErrRunningInput = errors.New("deviceproof: invalid running-info input")

// RunningMillis reproduces the fixed sample's signed UnixNano()/1_000_000
// expression. For contemporary positive timestamps it equals UnixMilli. It
// deliberately retains int64 nanosecond overflow and truncation toward zero,
// including for pre-epoch timestamps, rather than substituting UnixMilli.
func RunningMillis(now time.Time) int64 {
	return (now.Unix()*1_000_000_000 + int64(now.Nanosecond())) / 1_000_000
}

// MarshalWCSTF builds the inner proto2 WCSTF message. Each delta is the actual
// chosen value of one original rand.Int63n(10000) call, supplied explicitly for
// reproducibility. Count is the original caller's Go string byte length (Wxid),
// not the string contents or its Unicode rune count. The local bound avoids
// unbounded allocation and is above normal WeChat username lengths.
func MarshalWCSTF(now time.Time, count uint32, deltas []uint32) ([]byte, error) {
	if count > MaxRunningCount || uint64(len(deltas)) != uint64(count) {
		return nil, ErrRunningInput
	}
	start := RunningMillis(now)
	out := runningVarint(nil, 1, uint64(start))
	out = runningVarint(out, 2, uint64(start))
	out = runningVarint(out, 3, uint64(count))
	end := start
	for _, delta := range deltas {
		if delta >= 10000 {
			return nil, ErrRunningInput
		}
		end += int64(delta)
		// ABI tag is varint,4,rep without packed=true: one key per element.
		out = runningVarint(out, 4, uint64(end))
	}
	return out, nil
}

// MarshalWCSTE builds the inner proto2 WCSTE message. The original makes two
// time.Now calls: the first supplies seconds and the second nanoseconds divided
// by a million. Separate arguments preserve crossings of a second boundary.
func MarshalWCSTE(secondsNow, millisNow time.Time) []byte {
	seconds := uint32(secondsNow.Unix())
	millis := uint64(RunningMillis(millisNow))
	out := runningBytes(nil, 1, []byte("<LoginByID>"))
	out = runningVarint(out, 2, uint64(seconds))
	out = runningVarint(out, 3, uint64(seconds))
	out = runningVarint(out, 4, 0) // allocated required zero-valued pointer
	out = runningVarint(out, 5, 1)
	out = runningVarint(out, 6, 0) // allocated required zero-valued pointer
	out = runningVarint(out, 7, 0x0555015050505051)
	for tag := uint64(8); tag <= 11; tag++ {
		out = runningVarint(out, tag, millis)
	}
	return runningVarint(out, 12, 0x0554400000000000)
}

// BuildRunningInfo compresses and transforms a nonempty inner protobuf, then
// builds DeviceRunningInfoNew. timestamp must be the correct low 32-bit value
// chosen by the caller: milliseconds for WCSTF and seconds for WCSTE.
func BuildRunningInfo(plain []byte, timestamp uint32, cipher *sae08.Cipher) ([]byte, error) {
	if len(plain) == 0 || len(plain) > sae08.MaxPlaintext {
		return nil, ErrRunningInput
	}
	if cipher == nil {
		return nil, sae08.ErrTables
	}
	compressed, err := v08.Compress(plain)
	if err != nil {
		return nil, fmt.Errorf("deviceproof: compress running-info: %w", err)
	}
	encrypted, err := cipher.EncodeZipData(compressed)
	if err != nil {
		return nil, fmt.Errorf("deviceproof: encode running-info: %w", err)
	}
	out := runningBytes(nil, 1, []byte("00000008"))
	out = runningVarint(out, 2, 1)
	out = runningBytes(out, 3, encrypted)
	out = runningVarint(out, 4, uint64(timestamp))
	out = runningVarint(out, 5, 5)
	return runningVarint(out, 6, 0), nil // allocated required uint32 zero pointer
}

// BuildWCSTF returns the complete outer message consumed by WCExtInfo.wcstf.
func BuildWCSTF(now time.Time, count uint32, deltas []uint32, cipher *sae08.Cipher) ([]byte, error) {
	plain, err := MarshalWCSTF(now, count, deltas)
	if err != nil {
		return nil, err
	}
	return BuildRunningInfo(plain, uint32(RunningMillis(now)), cipher)
}

// BuildWCSTE returns the complete outer message consumed by WCExtInfo.wcste.
// Both original time samples are represented by now; use BuildWCSTEAt when the
// two clock readings must be supplied separately.
func BuildWCSTE(now time.Time, cipher *sae08.Cipher) ([]byte, error) {
	return BuildWCSTEAt(now, now, cipher)
}

// BuildWCSTEAt preserves the two distinct clock samples in the original helper.
func BuildWCSTEAt(secondsNow, millisNow time.Time, cipher *sae08.Cipher) ([]byte, error) {
	return BuildRunningInfo(MarshalWCSTE(secondsNow, millisNow), uint32(secondsNow.Unix()), cipher)
}

func runningVarint(out []byte, field, value uint64) []byte {
	out = binary.AppendUvarint(out, field<<3)
	return binary.AppendUvarint(out, value)
}

func runningBytes(out []byte, field uint64, value []byte) []byte {
	out = binary.AppendUvarint(out, field<<3|2)
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}
