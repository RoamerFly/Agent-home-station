//go:build !windows

package bot

import (
	"errors"
	"os"
	"syscall"
)

func lockState(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, errors.New("cannot open bot state lock")
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, errors.New("another bot owns this state directory")
	}
	return f, nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open checkpoint directory")
	}
	defer f.Close()
	if f.Sync() != nil {
		return errors.New("cannot sync checkpoint directory")
	}
	return nil
}
