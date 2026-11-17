package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jam-bm/edgephone/internal/agent"
	"github.com/jam-bm/edgephone/internal/config"
	"github.com/jam-bm/edgephone/internal/probe"
	"github.com/jam-bm/edgephone/internal/supervisor"
)

func TestExampleConfigIsValid(t *testing.T) {
	if _, err := config.Parse(exampleConfig); err != nil {
		t.Fatalf("embedded example.yaml is invalid: %v", err)
	}
}

func TestStatusAddr(t *testing.T) {
	for in, want := range map[string]string{
		":9100":          "127.0.0.1:9100",
		"0.0.0.0:9100":   "127.0.0.1:9100",
		"127.0.0.1:9200": "127.0.0.1:9200",
		"100.64.0.5:80":  "100.64.0.5:80",
	} {
		if got := statusAddr(in); got != want {
			t.Errorf("statusAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintStatus(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	ok := false
	rep := agent.StatusReport{
		Node: "galaxy-a30", Version: "v0.1.0", StartedAt: now.Add(-3*time.Hour - 12*time.Minute),
		Snapshot: probe.Snapshot{
			Battery: &probe.Battery{Percent: 78, TempC: 31.4, Plugged: true},
			Disk:    &probe.Disk{TotalBytes: 32 << 30, FreeBytes: 8 << 30},
		},
		Services: []supervisor.Status{{
			Name: "api", State: supervisor.StateRunning, PID: 4242, Restarts: 1,
			Healthy: &ok, HasHealth: true, Since: now.Add(-90 * time.Second), LastExit: "exit status 1",
		}},
	}
	var buf bytes.Buffer
	printStatus(&buf, rep, now)
	out := buf.String()
	for _, want := range []string{
		"galaxy-a30  edgephone v0.1.0  up 3h12m",
		"78%  31.4°C  plugged in, not charging",
		"8.0 GiB free of 32.0 GiB (25%)",
		"api", "failing", "4242", "exit status 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(path, []byte("1\n2\n3\n4\n"), 0o644)
	var buf bytes.Buffer
	n, err := tail(&buf, path, 2)
	if err != nil || n != 8 || buf.String() != "3\n4\n" {
		t.Fatalf("tail = %q, %d, %v", buf.String(), n, err)
	}
	buf.Reset()
	if _, err := tail(&buf, filepath.Join(t.TempDir(), "missing.log"), 5); err != nil || buf.Len() != 0 {
		t.Fatalf("missing file: %q %v", buf.String(), err)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[uint64]string{512: "512 B", 2048: "2.0 KiB", 3 << 30: "3.0 GiB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
