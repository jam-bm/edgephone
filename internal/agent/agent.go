// Package agent wires probes, the supervisor and the HTTP endpoints into the
// long-running `edgephone agent` process.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/jam-bm/edgephone/internal/config"
	"github.com/jam-bm/edgephone/internal/metrics"
	"github.com/jam-bm/edgephone/internal/probe"
	"github.com/jam-bm/edgephone/internal/supervisor"
)

// Collector produces probe snapshots.
type Collector interface {
	Collect(ctx context.Context) probe.Snapshot
}

// StatusReport is served at /status and printed by `edgephone status`.
type StatusReport struct {
	Node      string              `json:"node"`
	Version   string              `json:"version"`
	StartedAt time.Time           `json:"started_at"`
	Snapshot  probe.Snapshot      `json:"snapshot"`
	Services  []supervisor.Status `json:"services"`
}

// Agent is the edgephone daemon.
type Agent struct {
	cfg     *config.Config
	version string
	log     *slog.Logger
	prober  Collector
	sup     *supervisor.Supervisor
	started time.Time

	mu       sync.RWMutex
	snap     probe.Snapshot
	lastErrs map[string]string
}

// New builds an agent from config. Pass a nil prober to use the real one.
func New(cfg *config.Config, version string, logger *slog.Logger, prober Collector) *Agent {
	if prober == nil {
		prober = probe.New(cfg.Probes.DiskPath, cfg.Probes.Disabled)
	}
	return &Agent{
		cfg:      cfg,
		version:  version,
		log:      logger,
		prober:   prober,
		sup:      supervisor.New(cfg.Services, cfg.Logs, logger),
		started:  time.Now(),
		lastErrs: map[string]string{},
	}
}

// Snapshot implements metrics.Source.
func (a *Agent) Snapshot() probe.Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.snap
}

// Services implements metrics.Source.
func (a *Agent) Services() []supervisor.Status { return a.sup.Statuses() }

// Report returns the current status.
func (a *Agent) Report() StatusReport {
	return StatusReport{
		Node:      a.cfg.Node.Name,
		Version:   a.version,
		StartedAt: a.started,
		Snapshot:  a.Snapshot(),
		Services:  a.Services(),
	}
}

// Handler serves /metrics, /status and /healthz.
func (a *Agent) Handler() http.Handler {
	reg := metrics.NewRegistry(a, a.cfg.Node.Name, a.version, a.cfg.Probes.Disabled)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(a.Report())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "edgephone %s on %s\n\n/metrics  Prometheus metrics\n/status   JSON status\n/healthz  liveness\n",
			a.version, a.cfg.Node.Name)
	})
	return mux
}

// Run blocks until ctx is cancelled, then stops services and the server.
func (a *Agent) Run(ctx context.Context) error {
	// Bind first so a busy port fails immediately instead of after services start.
	ln, err := net.Listen("tcp", a.cfg.Metrics.Listen)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()
	a.log.Info("agent started", "version", a.version, "node", a.cfg.Node.Name,
		"listen", ln.Addr().String(), "services", len(a.cfg.Services))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.probeLoop(ctx) }()
	go func() { defer wg.Done(); a.sup.Run(ctx) }()

	select {
	case <-ctx.Done():
	case err = <-srvErr:
		a.log.Error("metrics server failed", "err", err)
	}

	a.log.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func (a *Agent) probeLoop(ctx context.Context) {
	tick := time.NewTicker(a.cfg.Probes.Interval)
	defer tick.Stop()
	for {
		a.probeOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *Agent) probeOnce(ctx context.Context) {
	s := a.prober.Collect(ctx)
	if ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	a.snap = s
	// Log only changes, so a missing Termux:API doesn't spam the log every 30s.
	for name, msg := range s.Errors {
		if a.lastErrs[name] != msg {
			a.log.Warn("probe failed", "probe", name, "err", msg)
		}
	}
	for name := range a.lastErrs {
		if _, still := s.Errors[name]; !still {
			a.log.Info("probe recovered", "probe", name)
		}
	}
	a.lastErrs = map[string]string{}
	for k, v := range s.Errors {
		a.lastErrs[k] = v
	}
	a.mu.Unlock()
}
