//go:build unix

package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/jam-bm/edgephone/internal/config"
)

// A service whose main process dies while a child still holds its stdout
// must be seen as exited right away, not after stop_timeout, and the child
// must be killed along with its process group.
func TestExitDetectedWhileChildHoldsLogPipe(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	svc := helper(t, "orphan")
	svc.Env["EDGEPHONE_PIDFILE"] = pidfile
	svc.StopTimeout = time.Minute
	sup := New([]config.Service{svc}, logsConfig(t), nil)
	start(t, sup)

	began := time.Now()
	waitFor(t, "a restart", func() bool { return sup.Statuses()[0].Restarts >= 1 })
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("exit noticed after %s; want well under stop_timeout", d)
	}

	data, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(string(data))
	waitFor(t, "orphaned child to be killed", func() bool {
		return syscall.Kill(pid, 0) != nil
	})
}
