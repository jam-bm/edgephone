package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jam-bm/edgephone/internal/config"
)

// The test binary doubles as the supervised process: when EDGEPHONE_HELPER
// is set it behaves as a tiny fake service instead of running tests. This
// keeps the tests portable (no reliance on sh, sleep or false).
func TestMain(m *testing.M) {
	switch os.Getenv("EDGEPHONE_HELPER") {
	case "":
		os.Exit(m.Run())
	case "crash":
		fmt.Println("hello from helper")
		os.Exit(3)
	case "sleep":
		fmt.Println("sleeping")
		time.Sleep(time.Hour)
	}
	os.Exit(99)
}

func helper(t *testing.T, mode string) config.Service {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.Service{
		Name: "svc-" + mode,
		Cmd:  []string{exe},
		Env:  map[string]string{"EDGEPHONE_HELPER": mode},
		Restart: config.Restart{
			BackoffInitial: 10 * time.Millisecond,
			BackoffMax:     40 * time.Millisecond,
			StableAfter:    time.Hour,
		},
		StopTimeout: 2 * time.Second,
	}
}

func logsConfig(t *testing.T) config.Logs {
	return config.Logs{Dir: t.TempDir(), MaxSizeMB: 1, MaxFiles: 2}
}

// start runs the supervisor in the background and returns a stop function
// that cancels it and waits for every process to exit.
func start(t *testing.T, sup *Supervisor) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("supervisor did not stop")
		}
	}
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			stop()
		}
	})
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCrashingServiceIsRestartedAndLogged(t *testing.T) {
	logs := logsConfig(t)
	sup := New([]config.Service{helper(t, "crash")}, logs, nil)
	stop := start(t, sup)

	waitFor(t, "3 restarts", func() bool { return sup.Statuses()[0].Restarts >= 3 })
	stop()

	st := sup.Statuses()[0]
	if st.State != StateStopped {
		t.Errorf("state after stop = %s", st.State)
	}
	if !strings.Contains(st.LastExit, "exit status 3") {
		t.Errorf("last exit = %q", st.LastExit)
	}
	data, err := os.ReadFile(filepath.Join(logs.Dir, "svc-crash.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hello from helper", "edgephone: started pid", "exit status 3; restarting in"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("log missing %q:\n%s", want, data)
		}
	}
}

func TestStopTerminatesRunningService(t *testing.T) {
	sup := New([]config.Service{helper(t, "sleep")}, logsConfig(t), nil)
	stop := start(t, sup)
	waitFor(t, "running", func() bool { return sup.Statuses()[0].State == StateRunning })

	pid := sup.Statuses()[0].PID
	if pid == 0 {
		t.Fatal("no pid while running")
	}
	began := time.Now()
	stop()
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("stop took %v", d)
	}
	if st := sup.Statuses()[0]; st.State != StateStopped || st.Restarts != 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestUnhealthyServiceIsRestarted(t *testing.T) {
	svc := helper(t, "sleep")
	svc.Health = &config.Health{Interval: 20 * time.Millisecond, Timeout: time.Second, Failures: 2}
	sup := New([]config.Service{svc}, logsConfig(t), nil)
	var checks atomic.Int32
	sup.services[0].checker = CheckerFunc(func(context.Context) error {
		checks.Add(1)
		return errors.New("boom")
	})
	start(t, sup)

	waitFor(t, "restart for health", func() bool { return sup.Statuses()[0].Restarts >= 1 })
	st := sup.Statuses()[0]
	if !strings.Contains(st.LastExit, "2 failed health checks") {
		t.Errorf("last exit = %q", st.LastExit)
	}
	if checks.Load() < 2 {
		t.Errorf("checks = %d", checks.Load())
	}
}

func TestHealthyServiceReportsUp(t *testing.T) {
	svc := helper(t, "sleep")
	svc.Health = &config.Health{Interval: 20 * time.Millisecond, Timeout: time.Second, Failures: 2}
	sup := New([]config.Service{svc}, logsConfig(t), nil)
	sup.services[0].checker = CheckerFunc(func(context.Context) error { return nil })
	start(t, sup)

	waitFor(t, "healthy", func() bool {
		st := sup.Statuses()[0]
		return st.Healthy != nil && *st.Healthy
	})
	if st := sup.Statuses()[0]; !st.Up() || st.Restarts != 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestStartPeriodIgnoresEarlyFailures(t *testing.T) {
	svc := helper(t, "sleep")
	svc.Health = &config.Health{Interval: 10 * time.Millisecond, Timeout: time.Second, Failures: 1, StartPeriod: time.Hour}
	sup := New([]config.Service{svc}, logsConfig(t), nil)
	var checks atomic.Int32
	sup.services[0].checker = CheckerFunc(func(context.Context) error { checks.Add(1); return errors.New("warming up") })
	start(t, sup)

	waitFor(t, "several checks", func() bool { return checks.Load() >= 5 })
	st := sup.Statuses()[0]
	if st.Restarts != 0 || st.Up() {
		t.Errorf("status = %+v; want running, unhealthy, not restarted", st)
	}
}

func TestMissingBinaryBacksOff(t *testing.T) {
	svc := helper(t, "sleep")
	svc.Cmd = []string{filepath.Join(t.TempDir(), "does-not-exist")}
	sup := New([]config.Service{svc}, logsConfig(t), nil)
	start(t, sup)
	waitFor(t, "restart", func() bool { return sup.Statuses()[0].Restarts >= 2 })
	if st := sup.Statuses()[0]; !strings.Contains(st.LastExit, "failed to start") {
		t.Errorf("last exit = %q", st.LastExit)
	}
}

func TestBackoff(t *testing.T) {
	s := time.Second
	cases := []struct {
		attempt int
		want    time.Duration
	}{{0, s}, {1, 2 * s}, {2, 4 * s}, {6, 64 * s}, {7, 120 * s}, {100, 120 * s}}
	for _, c := range cases {
		if got := Backoff(s, 2*time.Minute, c.attempt); got != c.want {
			t.Errorf("Backoff(attempt=%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}

func TestHTTPChecker(t *testing.T) {
	var healthy atomic.Bool
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c := NewChecker(config.Health{HTTP: "http://" + ln.Addr().String() + "/healthz"}, "")
	if err := c.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("want 503 error, got %v", err)
	}
	healthy.Store(true)
	if err := c.Check(context.Background()); err != nil {
		t.Errorf("want healthy, got %v", err)
	}

	tcp := NewChecker(config.Health{TCP: ln.Addr().String()}, "")
	if err := tcp.Check(context.Background()); err != nil {
		t.Errorf("tcp check: %v", err)
	}
}

func TestRotatingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.log")
	r, err := OpenRotating(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"aaaaaaa\n", "bbbbbbb\n", "ccccccc\n", "ddddddd\n"} {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()

	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	if got := read(path); got != "ddddddd\n" {
		t.Errorf("current = %q", got)
	}
	if got := read(path + ".1"); got != "ccccccc\n" {
		t.Errorf(".1 = %q", got)
	}
	if got := read(path + ".2"); got != "bbbbbbb\n" {
		t.Errorf(".2 = %q", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf(".3 should not exist (max_files=2)")
	}
}
