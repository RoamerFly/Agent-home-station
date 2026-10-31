package adbserver

import "errors"

var errCleanupUnconfirmed = errors.New("adb server cleanup unconfirmed")

type reapedProcess interface {
	reaped() bool
	waitError() error
}
