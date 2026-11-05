//go:build linux

package admin

import "syscall"

// Agent-created FIFOs cannot stall control-plane integrity checks.
const repairNonblockingFlag = syscall.O_NONBLOCK
