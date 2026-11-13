package station

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
)

// failureMessage keeps owner-facing Chinese causes intact but replaces
// filesystem, process and protocol details with a stable sentence. Handlers
// must never return absolute home paths, errno text or browser profile paths.
func failureMessage(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	var sysErr *os.SyscallError
	if errors.As(err, &pathErr) || errors.As(err, &linkErr) || errors.As(err, &sysErr) {
		return fallback
	}
	message := err.Error()
	if message == "" || looksInternal(message) {
		return fallback
	}
	return message
}

// looksInternal recognises the shapes that only the runtime produces: Windows
// paths, OS/syscall wording, CDP addresses and exit codes. Chinese owner-facing
// sentences never contain these.
func looksInternal(message string) bool {
	internal := []string{`\`, "no such file", "Access is denied", "The system cannot find",
		"directory not empty", "The process cannot access", "0x", "127.0.0.1:", "localhost:"}
	for _, marker := range internal {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// logFailure keeps the raw cause in the local log for diagnosis while the
// response body stays stable.
func logFailure(op string, err error) {
	if err != nil {
		log.Printf("%s: %v", op, err)
	}
}
