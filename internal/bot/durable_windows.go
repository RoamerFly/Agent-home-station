package bot

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func replaceQueueCheckpoint(staged, current string) error {
	// Windows can deny replacement while a receipt reader or scanner has
	// the checkpoint open. Retry only the rename, keeping both snapshots
	// intact until the replacement succeeds.
	// The desktop UI and delivery-evidence readers can hold the checkpoint
	// briefly without coordinating with the worker. Keep the staged snapshot
	// intact and retry the replace for a bounded window instead of failing the
	// whole Agent while a file attachment is in readback.
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := os.Rename(staged, current)
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		time.Sleep(min(25*time.Millisecond, remaining))
	}
}
