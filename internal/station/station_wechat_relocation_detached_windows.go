package station

import (
	"encoding/binary"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func relocationSourceAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_INVALID_DRIVE) ||
		errors.Is(err, windows.ERROR_NOT_READY)
}

func sealLegacyDetachedFence(worker *os.Root, digest string) ([]byte, error) {
	return legacyDetachedFenceDPAPI(worker, []byte(digest), true)
}

func openLegacyDetachedFence(worker *os.Root, encrypted []byte) (string, error) {
	raw, err := legacyDetachedFenceDPAPI(worker, encrypted, false)
	return string(raw), err
}

func legacyDetachedFenceDPAPI(worker *os.Root, input []byte, seal bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, os.ErrInvalid
	}
	dir, err := worker.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(windows.Handle(dir.Fd()), &info)
	if err != nil {
		return nil, err
	}
	if info.FileIndexHigh == 0 && info.FileIndexLow == 0 {
		return nil, errors.New("persistent directory identity unavailable")
	}
	// Volume and file ID reject a copied worker even if it replaces the
	// original at the same pathname on this PC. DPAPI binds to the machine.
	entropy := make([]byte, 12)
	binary.LittleEndian.PutUint32(entropy[0:4], info.VolumeSerialNumber)
	binary.LittleEndian.PutUint32(entropy[4:8], info.FileIndexHigh)
	binary.LittleEndian.PutUint32(entropy[8:12], info.FileIndexLow)
	data := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	extra := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var result windows.DataBlob
	flags := uint32(windows.CRYPTPROTECT_UI_FORBIDDEN)
	if seal {
		err = windows.CryptProtectData(&data, nil, &extra, 0, nil, flags|windows.CRYPTPROTECT_LOCAL_MACHINE, &result)
	} else {
		err = windows.CryptUnprotectData(&data, nil, &extra, 0, nil, flags, &result)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(result.Data))))
	if result.Size == 0 || result.Size > legacyDetachedFenceLimit || result.Data == nil {
		return nil, os.ErrInvalid
	}
	return append([]byte(nil), unsafe.Slice(result.Data, int(result.Size))...), nil
}
