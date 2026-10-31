//go:build !windows

package devicebroker

import (
	"errors"
	"os"
	"syscall"
)

// ensurePrivateDirectory creates only the final lease-directory leaf. The
// trusted parent already exists and is checked by resolve; no historical
// workspace directory is recursively chmod'ed.
func ensurePrivateDirectory(path string) (string, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return "", err
	}
	if err := verifyPrivateDirectory(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func verifyPrivateDirectory(path string) error {
	canonical, err := canonicalDirectory(path)
	if err != nil || !samePath(canonical, path) {
		return errors.New("private directory changed")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("private directory owner mismatch")
	}
	if info.Mode().Perm() != 0700 {
		return errors.New("private directory mode mismatch")
	}
	return nil
}
