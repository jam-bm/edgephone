//go:build linux

package probe

import "testing"

func TestStatDisk(t *testing.T) {
	d, err := StatDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalBytes == 0 || d.FreeBytes > d.TotalBytes {
		t.Fatalf("disk = %+v", d)
	}
}
