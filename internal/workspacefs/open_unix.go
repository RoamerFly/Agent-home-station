//go:build unix

package workspacefs

import "syscall"

const nonblockingFlag = syscall.O_NONBLOCK
