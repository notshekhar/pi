//go:build linux

package tui

import (
	"os"
	"syscall"
	"unsafe"
)

func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

type winsize struct{ row, col, xpixel, ypixel uint16 }

// Size returns the terminal grid for f, defaulting to 80x24.
func size(f *os.File) Size {
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 || ws.col == 0 || ws.row == 0 {
		return Size{Cols: 80, Rows: 24}
	}
	return Size{Cols: int(ws.col), Rows: int(ws.row)}
}

func makeRaw(fd int) (restore func(), err error) {
	var old syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&old))); errno != 0 {
		return nil, errno
	}
	raw := old
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Iflag &^= syscall.IXON | syscall.IXOFF | syscall.ICRNL | syscall.INLCR
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&raw))); errno != 0 {
		return nil, errno
	}
	return func() {
		syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&old)))
	}, nil
}
