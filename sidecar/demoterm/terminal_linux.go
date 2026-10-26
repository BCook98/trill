package demoterm

import "syscall"

const getTermios = syscall.TCGETS
const setTermios = syscall.TCSETS

func selectRead(n int, set *syscall.FdSet, timeout *syscall.Timeval) error {
	_, err := syscall.Select(n, set, nil, nil, timeout)
	return err
}
