//go:build linux

package probe

import "syscall"

// StatDisk reports filesystem usage. It works on linux and android (GOOS=android implies the linux tag).
func StatDisk(path string) (*Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil, err
	}
	bs := uint64(st.Bsize)
	return &Disk{Path: path, TotalBytes: st.Blocks * bs, FreeBytes: st.Bavail * bs}, nil
}
