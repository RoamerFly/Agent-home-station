//go:build !windows

package admin

import "errors"

func localComfyListenerOwner(port, expectedPID int) error {
	return errors.New("station_comfyui_windows_only")
}
