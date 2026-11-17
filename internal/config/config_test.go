package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
node: { name: galaxy-a30 }
services:
  - name: api
    cmd: ["./api"]
    health: { http: "http://127.0.0.1:8080/healthz" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node.Name != "galaxy-a30" {
		t.Errorf("node name = %q", cfg.Node.Name)
	}
	if cfg.Metrics.Listen != "127.0.0.1:9100" {
		t.Errorf("listen = %q", cfg.Metrics.Listen)
	}
	s := cfg.Services[0]
	if s.Restart.BackoffInitial != time.Second || s.Restart.BackoffMax != 2*time.Minute {
		t.Errorf("restart defaults = %+v", s.Restart)
	}
	if s.Health.Interval != 15*time.Second || s.Health.Failures != 3 || s.Health.Timeout != 5*time.Second {
		t.Errorf("health defaults = %+v", s.Health)
	}
}

func TestParseEmptyDocument(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Probes.Interval != 30*time.Second {
		t.Errorf("probe interval = %v", cfg.Probes.Interval)
	}
}

func TestParseDurations(t *testing.T) {
	cfg, err := Parse([]byte(`
services:
  - name: api
    cmd: [x]
    restart: { backoff_initial: 500ms, backoff_max: 30s }
probes: { interval: 1m }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Services[0].Restart.BackoffMax; got != 30*time.Second {
		t.Errorf("backoff_max = %v", got)
	}
	if cfg.Probes.Interval != time.Minute {
		t.Errorf("interval = %v", cfg.Probes.Interval)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("metrics: { listne: ':9100' }"))
	if err == nil || !strings.Contains(err.Error(), "listne") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestValidateCollectsAllErrors(t *testing.T) {
	_, err := Parse([]byte(`
services:
  - name: api
    cmd: [x]
    health: { http: "http://x", tcp: "127.0.0.1:1" }
  - name: api
    cmd: []
  - name: "bad name"
    cmd: [x]
probes: { disabled: [gps] }
`))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{
		"exactly one of http, tcp or exec",
		"duplicate name",
		"cmd is required",
		"may only contain",
		`unknown probe "gps"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}
