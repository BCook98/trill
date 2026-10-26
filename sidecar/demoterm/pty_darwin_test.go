package demoterm

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

type termiosForTest = syscall.Termios

func openPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	for _, op := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK} {
		if err := ioctl(master.Fd(), op, nil); err != nil {
			t.Fatalf("initialize test PTY: %v", err)
		}
	}
	var name [128]byte
	if err := ioctl(master.Fd(), syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		t.Fatal(err)
	}
	end := bytes.IndexByte(name[:], 0)
	if end < 0 {
		t.Fatal("invalid PTY name")
	}
	slave, err := os.OpenFile(string(name[:end]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}
