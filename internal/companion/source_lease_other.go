//go:build !windows && !linux

package companion

import "errors"

func verifyLegacyMemoryOffline(string) error {
	return errors.New("verified offline memory maintenance is supported on Windows and Linux")
}
