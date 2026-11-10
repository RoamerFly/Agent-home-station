//go:build !windows

package station

import (
	"errors"
	"os"
)

func relocationSourceAbsent(err error) bool { return errors.Is(err, os.ErrNotExist) }

func sealLegacyDetachedFence(_ *os.Root, _ string) ([]byte, error) { return nil, os.ErrInvalid }

func openLegacyDetachedFence(_ *os.Root, _ []byte) (string, error) { return "", os.ErrInvalid }
