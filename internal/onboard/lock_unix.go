//go:build unix

package onboard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func (p Paths) LockFile() string { return filepath.Join(p.ConfigDir(), "lock") }

func lockConfig(p Paths) (func(), error) {
	if err := os.MkdirAll(p.ConfigDir(), 0o700); err != nil {
		return nil, err
	}
	return lockAt(p.LockFile())
}

func lockAt(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another reright install or uninstall has held %s for two minutes. Wait for it to finish, or remove that file if no reright process is running", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
