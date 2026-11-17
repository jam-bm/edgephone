//go:build !linux

package probe

import (
	"errors"
	"runtime"
)

// StatDisk is only implemented for linux/android; other platforms are for
// development only.
func StatDisk(string) (*Disk, error) {
	return nil, errors.New("disk probe not supported on " + runtime.GOOS)
}
