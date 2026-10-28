//go:build !darwin && !freebsd && !netbsd && !openbsd && !linux

package tui

import "os"

func isTerminal(f *os.File) bool { return false }

func size(f *os.File) Size { return Size{Cols: 80, Rows: 24} }

func makeRaw(fd int) (func(), error) {
	return func() {}, errNotTTY
}

var errNotTTY = errString("not a tty")

type errString string

func (e errString) Error() string { return string(e) }
