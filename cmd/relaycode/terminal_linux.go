//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(&termios)))
	return errno == 0
}
