//go:build linux || darwin

package demoterm

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func ioctl(fd uintptr, command uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, command, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func rawMode(input *os.File) (func() error, error) {
	var saved syscall.Termios
	if err := ioctl(input.Fd(), getTermios, unsafe.Pointer(&saved)); err != nil {
		return nil, fmt.Errorf("demo terminal requires a PTY: %w", err)
	}
	raw := saved
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON | syscall.PARMRK | syscall.INLCR | syscall.IGNCR
	raw.Oflag &^= syscall.OPOST
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(input.Fd(), setTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, fmt.Errorf("set demo terminal raw mode: %w", err)
	}
	return func() error { return ioctl(input.Fd(), setTermios, unsafe.Pointer(&saved)) }, nil
}

func terminalSize(input *os.File) (columns, rows int, err error) {
	var size struct{ Rows, Columns, X, Y uint16 }
	if err = ioctl(input.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
		return 0, 0, err
	}
	columns, rows = int(size.Columns), int(size.Rows)
	// An uninitialized PTY has zero dimensions. Keep both rendering and IPC
	// bounded even if the terminal reports an unreasonable size.
	if columns == 0 {
		columns = 80
	}
	if rows == 0 {
		rows = 24
	}
	columns = min(columns, 1000)
	rows = min(rows, 1000)
	return columns, rows, nil
}

func readable(input *os.File) (bool, error) {
	fd := int(input.Fd())
	var set syscall.FdSet
	bits := int(unsafe.Sizeof(set.Bits[0])) * 8
	if fd < 0 || fd >= len(set.Bits)*bits {
		return false, fmt.Errorf("demo terminal input descriptor out of range")
	}
	set.Bits[fd/bits] |= 1 << uint(fd%bits)
	timeout := syscall.Timeval{Usec: 25000}
	if err := selectRead(fd+1, &set, &timeout); err != nil {
		return false, err
	}
	return set.Bits[fd/bits]&(1<<uint(fd%bits)) != 0, nil
}
