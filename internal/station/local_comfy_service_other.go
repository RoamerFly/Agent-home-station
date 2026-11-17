//go:build !windows

package station

import "errors"

func localComfyListenerOwner(port, expectedPID int) error {
	return errors.New("station_comfyui_windows_only")
}

func localOBSListenerOwner(port, expectedPID int) error {
	return localComfyListenerOwner(port, expectedPID)
}
