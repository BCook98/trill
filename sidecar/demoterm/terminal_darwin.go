package demoterm

import "syscall"

const getTermios = syscall.TIOCGETA
const setTermios = syscall.TIOCSETA

func selectRead(n int, set *syscall.FdSet, timeout *syscall.Timeval) error {
	return syscall.Select(n, set, nil, nil, timeout)
}
