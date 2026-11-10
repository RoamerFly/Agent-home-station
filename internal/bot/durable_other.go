//go:build !windows

package bot

import "os"

func replaceQueueCheckpoint(staged, current string) error {
	return os.Rename(staged, current)
}
