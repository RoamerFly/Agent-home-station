//go:build !linux && !darwin && !freebsd && !windows

package admin

import "errors"

func availableStorageBytes(string) (uint64, error) {
	return 0, errors.New("storage_capacity_unavailable_on_platform")
}
