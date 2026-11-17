// Package supervisor runs services, health-checks them, and restarts them
// with exponential backoff.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jam-bm/edgephone/internal/config"
)

// State is the lifecycle state of a service.
type State string

// Service states.
const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateBackoff  State = "backoff"
	StateStopped  State = "stopped"
)

// Status is a point-in-time view of one service.
type Status struct {
	Name     string    `json:"name"`
	State    State     `json:"state"`
	PID      int       `json:"pid,omitempty"`
	Restarts int       `json:"restarts"`
	Since    time.Time `json:"since"`
	// Healthy is nil until the first health check completes, or when the
	// service has no health check.
	Healthy    *bool     `json:"healthy,omitempty"`
	HealthErr  string    `json:"health_error,omitempty"`
	LastExit   string    `json:"last_exit,omitempty"`
	NextStart  time.Time `json:"next_start,omitempty"`
	HasHealth  bool      `json:"has_health"`
	LogFile    string    `json:"log_file"`
	StartError string    `json:"start_error,omitempty"`
}

// Up reports whether the process is running and not known to be unhealthy.
func (s Status) Up() bool {
	return s.State == StateRunning && (s.Healthy == nil || *s.Healthy)
}

var errUnhealthy = errors.New("health check failed")

// Supervisor owns a set of services.
type Supervisor struct {
	services []*service
	log      *slog.Logger
}

// New prepares services from config. Nothing starts until Run.
func New(services []config.Service, logs config.Logs, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Supervisor{log: logger}
	for _, cfg := range services {
		svc := &service{
			cfg:      cfg,
			log:      logger.With("service", cfg.Name),
			logPath:  filepath.Join(logs.Dir, cfg.Name+".log"),
			maxBytes: int64(logs.MaxSizeMB) << 20,
			maxFiles: logs.MaxFiles,
			status:   Status{Name: cfg.Name, State: StateStopped, HasHealth: cfg.Health != nil},
		}
		svc.status.LogFile = svc.logPath
		if cfg.Health != nil {
			svc.checker = NewChecker(*cfg.Health, cfg.Workdir)
		}
		s.services = append(s.services, svc)
	}
	return s
}

// Run starts every service and blocks until ctx is cancelled and all
// processes have exited.
func (s *Supervisor) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, svc := range s.services {
		wg.Add(1)
		go func(svc *service) {
			defer wg.Done()
			svc.run(ctx)
		}(svc)
	}
	wg.Wait()
}

// Statuses returns a snapshot of every service, sorted by name.
func (s *Supervisor) Statuses() []Status {
	out := make([]Status, 0, len(s.services))
	for _, svc := range s.services {
		out = append(out, svc.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type service struct {
	cfg      config.Service
	checker  Checker
	log      *slog.Logger
	logPath  string
	maxBytes int64
	maxFiles int

	mu     sync.Mutex
	status Status
}

func (s *service) snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	if st.Healthy != nil {
		h := *st.Healthy
		st.Healthy = &h
	}
	return st
}

func (s *service) update(fn func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.status)
}

func (s *service) run(ctx context.Context) {
	out, err := OpenRotating(s.logPath, s.maxBytes, s.maxFiles)
	var w io.Writer = io.Discard
	if err != nil {
		s.log.Error("cannot open log file; service output will be discarded", "path", s.logPath, "err", err)
	} else {
		w = out
		defer out.Close()
	}
	event := func(format string, a ...any) {
		fmt.Fprintf(w, "%s edgephone: %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}

	attempt := 0
	for {
		started := time.Now()
		err := s.runOnce(ctx, w, event)
		if ctx.Err() != nil {
			event("stopped")
			s.update(func(st *Status) {
				st.State, st.PID, st.Healthy, st.Since = StateStopped, 0, nil, time.Now()
			})
			return
		}
		if time.Since(started) >= s.cfg.Restart.StableAfter {
			attempt = 0
		}
		delay := Backoff(s.cfg.Restart.BackoffInitial, s.cfg.Restart.BackoffMax, attempt)
		attempt++

		exit := "exited with status 0"
		if err != nil {
			exit = err.Error()
		}
		event("%s; restarting in %s", exit, delay)
		s.log.Warn("service exited", "reason", exit, "restart_in", delay)
		s.update(func(st *Status) {
			st.State, st.PID, st.Healthy, st.HealthErr = StateBackoff, 0, nil, ""
			st.LastExit, st.Restarts = exit, st.Restarts+1
			st.Since, st.NextStart = time.Now(), time.Now().Add(delay)
		})

		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			event("stopped")
			s.update(func(st *Status) { st.State, st.Since, st.NextStart = StateStopped, time.Now(), time.Time{} })
			return
		case <-t.C:
		}
	}
}

// runOnce starts the process and waits for it to exit, is killed for
// failing health checks, or is stopped because ctx was cancelled.
func (s *service) runOnce(ctx context.Context, out io.Writer, event func(string, ...any)) error {
	s.update(func(st *Status) {
		st.State, st.Since, st.NextStart, st.StartError = StateStarting, time.Now(), time.Time{}, ""
	})

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	argv := s.cfg.Cmd
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = s.cfg.Workdir
	cmd.Env = os.Environ()
	for k, v := range s.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Give the child the write end of our own pipe instead of an io.Writer.
	// With an io.Writer, exec.Cmd.Wait also waits for every process holding
	// the pipe, so a leftover child (`sh -c 'worker & wait'` after the shell
	// is killed) would hide the exit until WaitDelay. With an *os.File, Wait
	// returns when the main process exits; cleanupGroup then kills the rest
	// of the group, which closes the pipe and ends the copy below.
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	setProcAttr(cmd)
	// On cancel: SIGTERM first, SIGKILL after StopTimeout.
	cmd.Cancel = func() error { return terminate(cmd) }
	cmd.WaitDelay = s.cfg.StopTimeout

	err = cmd.Start()
	pw.Close() // the child has its own copy now
	if err != nil {
		pr.Close()
		err = fmt.Errorf("failed to start: %w", err)
		s.update(func(st *Status) { st.StartError = err.Error() })
		return err
	}
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(out, pr); close(copied) }()
	defer func() {
		// Normally the pipe closes as soon as the group is killed. A process
		// that left the group (setsid, double fork) can keep it open; don't
		// let that block restarts.
		select {
		case <-copied:
		case <-time.After(time.Second):
		}
		pr.Close()
	}()
	pid := cmd.Process.Pid
	event("started pid %d: %q", pid, argv)
	s.log.Info("service started", "pid", pid)
	s.update(func(st *Status) { st.State, st.PID, st.Since = StateRunning, pid, time.Now() })

	var wg sync.WaitGroup
	if s.checker != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.monitor(runCtx, cancel, event)
		}()
	}

	err = cmd.Wait()
	cleanupGroup(pid) // reap anything the service left behind in its process group
	cancel(nil)
	wg.Wait()

	if ctx.Err() == nil && errors.Is(context.Cause(runCtx), errUnhealthy) {
		return fmt.Errorf("killed after %d failed health checks", s.cfg.Health.Failures)
	}
	return err
}

func (s *service) monitor(ctx context.Context, kill context.CancelCauseFunc, event func(string, ...any)) {
	h := s.cfg.Health
	started := time.Now()
	failures := 0
	tick := time.NewTicker(h.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		cctx, cancel := context.WithTimeout(ctx, h.Timeout)
		err := s.checker.Check(cctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			failures = 0
			ok := true
			s.update(func(st *Status) { st.Healthy, st.HealthErr = &ok, "" })
			continue
		}
		bad := false
		s.update(func(st *Status) { st.Healthy, st.HealthErr = &bad, err.Error() })
		if time.Since(started) < h.StartPeriod {
			continue // still warming up
		}
		failures++
		event("health check failed (%d/%d): %v", failures, h.Failures, err)
		if failures >= h.Failures {
			s.log.Warn("restarting unhealthy service", "failures", failures, "err", err)
			kill(errUnhealthy)
			return
		}
	}
}
