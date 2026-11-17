package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jam-bm/edgephone/internal/config"
	"github.com/jam-bm/edgephone/internal/probe"
)

type fakeProber struct{ calls atomic.Int32 }

func (f *fakeProber) Collect(context.Context) probe.Snapshot {
	f.calls.Add(1)
	return probe.Snapshot{
		Time:    time.Now(),
		Battery: &probe.Battery{Percent: 64, TempC: 33, Plugged: true},
		Errors:  map[string]string{"wifi": "termux-wifi-connectioninfo timed out"},
	}
}

func freeAddr(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func get(t *testing.T, url string) string {
	t.Helper()
	var lastErr error
	for i := 0; i < 100; i++ {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("GET %s: %s", url, resp.Status)
			}
			return string(b)
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, lastErr)
	return ""
}

// End-to-end: the agent serves metrics and status from a (fake) prober and
// shuts down cleanly when its context is cancelled.
func TestAgentServesMetricsAndStatus(t *testing.T) {
	cfg, err := config.Parse([]byte(fmt.Sprintf(`
node: { name: test-phone }
metrics: { listen: %q }
probes: { interval: 1s }
logs: { dir: %q }
`, freeAddr(t), t.TempDir())))
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeProber{}
	a := New(cfg, "v0.0.0-test", slog.New(slog.NewTextHandler(io.Discard, nil)), fp)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	base := "http://" + cfg.Metrics.Listen
	if body := get(t, base+"/healthz"); strings.TrimSpace(body) != "ok" {
		t.Errorf("healthz = %q", body)
	}

	// The first probe runs right after the listener opens; wait for it.
	var m string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m = get(t, base+"/metrics"); strings.Contains(m, "edgephone_battery_percent") {
			break
		}
	}
	for _, want := range []string{
		"edgephone_battery_percent 64",
		`edgephone_probe_success{probe="wifi"} 0`,
		`edgephone_build_info{goarch=`,
		`node="test-phone"`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}

	var rep StatusReport
	if err := json.Unmarshal([]byte(get(t, base+"/status")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Node != "test-phone" || rep.Version != "v0.0.0-test" || rep.Snapshot.Battery.Percent != 64 {
		t.Errorf("status = %+v", rep)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not shut down")
	}
}

func TestAgentFailsFastWhenPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg, _ := config.Parse([]byte(fmt.Sprintf("metrics: { listen: %q }\nlogs: { dir: %q }", ln.Addr().String(), t.TempDir())))
	a := New(cfg, "dev", slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeProber{})
	if err := a.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "metrics listener") {
		t.Fatalf("err = %v", err)
	}
}
