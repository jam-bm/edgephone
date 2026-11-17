package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jam-bm/edgephone/internal/probe"
	"github.com/jam-bm/edgephone/internal/supervisor"
)

type fakeSource struct {
	snap probe.Snapshot
	svcs []supervisor.Status
}

func (f fakeSource) Snapshot() probe.Snapshot      { return f.snap }
func (f fakeSource) Services() []supervisor.Status { return f.svcs }

func TestCollectorOutput(t *testing.T) {
	healthy := true
	usage := 0.25
	src := fakeSource{
		snap: probe.Snapshot{
			Time:    time.Unix(1700000000, 0),
			Battery: &probe.Battery{Percent: 78, TempC: 31.5, Charging: true, Plugged: true},
			Wifi:    &probe.Wifi{RSSI: -58, LinkSpeedMbps: 433, Connected: true},
			CPU:     &probe.CPU{Load1: 1, Load5: 2, Load15: 3, Usage: &usage},
			Errors:  map[string]string{"memory": "boom"},
		},
		svcs: []supervisor.Status{
			{Name: "api", State: supervisor.StateRunning, Restarts: 2, Healthy: &healthy},
			{Name: "worker", State: supervisor.StateBackoff, Restarts: 5},
		},
	}
	reg := NewRegistry(src, "galaxy-a30", "v0.1.0", []string{"disk"})

	want := `
# HELP edgephone_battery_percent Battery charge level, 0-100.
# TYPE edgephone_battery_percent gauge
edgephone_battery_percent 78
# HELP edgephone_battery_temp_celsius Battery temperature in degrees Celsius.
# TYPE edgephone_battery_temp_celsius gauge
edgephone_battery_temp_celsius 31.5
# HELP edgephone_cpu_usage_ratio CPU busy time ratio since the previous probe (0-1). Absent when /proc/stat is unreadable.
# TYPE edgephone_cpu_usage_ratio gauge
edgephone_cpu_usage_ratio 0.25
# HELP edgephone_probe_success 1 if the probe succeeded in the latest round, 0 if it failed.
# TYPE edgephone_probe_success gauge
edgephone_probe_success{probe="battery"} 1
edgephone_probe_success{probe="cpu"} 1
edgephone_probe_success{probe="memory"} 0
edgephone_probe_success{probe="wifi"} 1
# HELP edgephone_service_restarts_total Number of times the supervisor restarted the service.
# TYPE edgephone_service_restarts_total counter
edgephone_service_restarts_total{service="api"} 2
edgephone_service_restarts_total{service="worker"} 5
# HELP edgephone_service_up 1 if the service is running and not failing health checks.
# TYPE edgephone_service_up gauge
edgephone_service_up{service="api"} 1
edgephone_service_up{service="worker"} 0
`
	err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"edgephone_battery_percent", "edgephone_battery_temp_celsius", "edgephone_cpu_usage_ratio",
		"edgephone_probe_success", "edgephone_service_restarts_total", "edgephone_service_up")
	if err != nil {
		t.Fatal(err)
	}

	// Lint catches naming mistakes (units, _total suffixes, ...).
	problems, err := testutil.GatherAndLint(reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		if strings.HasPrefix(p.Metric, "edgephone_") {
			t.Errorf("lint: %s: %s", p.Metric, p.Text)
		}
	}
}

func TestNoProbeMetricsBeforeFirstRound(t *testing.T) {
	reg := NewRegistry(fakeSource{}, "n", "dev", nil)
	n, err := testutil.GatherAndCount(reg, "edgephone_probe_success", "edgephone_battery_percent")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("got %d probe series before any probe ran", n)
	}
}
