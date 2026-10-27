package push

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func ValidCredential(value string) bool { return len(value) == 64 && hexToken.MatchString(value) }

// Refuse links, shared files and broadly readable secrets; errors never include content.
func ReadPrivateFile(path string) ([]byte, error) {
	failure := errors.New("push configuration requires an owned, regular 0600 file at an absolute path")
	if !filepath.IsAbs(path) {
		return nil, failure
	}
	descriptor, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, failure
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, failure
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, failure
	}
	contents, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(contents) > 16384 {
		return nil, failure
	}
	return contents, nil
}
