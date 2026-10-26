package demoapp

import (
	"net"
	"syscall"
	"testing"
	"time"
)

func TestListenerBoundsBeforeHeadersAndCloses(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := limitListener(raw, 1)
	defer l.Close()
	c1, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	a1, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()
	c2, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	done := make(chan error, 1)
	go func() {
		c, e := l.Accept()
		if c != nil {
			c.Close()
		}
		done <- e
	}()
	select {
	case <-done:
		t.Fatal("accepted above socket bound")
	case <-time.After(20 * time.Millisecond):
	}
	l.Close()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("close should abort blocked accept")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock accept")
	}
}
func TestOwnershipLockNotInherited(t *testing.T) {
	root := ownedTestRoot(t)
	lock, err := stateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, lock.Fd(), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("lock descriptor can leak into child", errno)
	}
}
