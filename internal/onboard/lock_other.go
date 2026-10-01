//go:build !unix

package onboard

import (
	"os"
	"path/filepath"
)

func (p Paths) LockFile() string { return filepath.Join(p.ConfigDir(), "lock") }

func lockConfig(p Paths) (func(), error) {
	if err := os.MkdirAll(p.ConfigDir(), 0o700); err != nil {
		return nil, err
	}
	return func() {}, nil
}
