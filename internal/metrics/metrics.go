// Package metrics exposes probe snapshots and service status as
// Prometheus metrics.
package metrics

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/jam-bm/edgephone/internal/config"
	"github.com/jam-bm/edgephone/internal/probe"
	"github.com/jam-bm/edgephone/internal/supervisor"
)

// Source provides the latest state to export. It is read on every scrape.
type Source interface {
	Snapshot() probe.Snapshot
	Services() []supervisor.Status
}

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc("edgephone_"+name, help, labels, nil)
}

var (
	buildInfo     = desc("build_info", "Build and node information. Always 1.", "version", "goos", "goarch", "node")
	probeSuccess  = desc("probe_success", "1 if the probe succeeded in the latest round, 0 if it failed.", "probe")
	lastProbe     = desc("last_probe_timestamp_seconds", "Unix time of the latest probe round.")
	batteryPct    = desc("battery_percent", "Battery charge level, 0-100.")
	batteryTemp   = desc("battery_temp_celsius", "Battery temperature in degrees Celsius.")
	batteryChg    = desc("battery_charging", "1 if the battery is charging.")
	batteryPlug   = desc("battery_plugged", "1 if a charger is connected (even if the battery is full).")
	wifiRSSI      = desc("wifi_rssi_dbm", "Wi-Fi signal strength in dBm.")
	wifiSpeed     = desc("wifi_link_speed_mbps", "Wi-Fi link speed in Mbit/s.")
	wifiConnected = desc("wifi_connected", "1 if Wi-Fi is associated.")
	load          = desc("load_average", "System load average.", "period")
	cpuUsage      = desc("cpu_usage_ratio", "CPU busy time ratio since the previous probe (0-1). Absent when /proc/stat is unreadable.")
	memTotal      = desc("memory_total_bytes", "Total RAM.")
	memAvail      = desc("memory_available_bytes", "RAM available for new allocations.")
	diskTotal     = desc("disk_total_bytes", "Size of the filesystem holding the data directory.", "path")
	diskFree      = desc("disk_free_bytes", "Free space on that filesystem available to edgephone.", "path")
	serviceUp     = desc("service_up", "1 if the service is running and not failing health checks.", "service")
	serviceHealth = desc("service_healthy", "1 if the latest health check passed, 0 if it failed. Absent before the first check.", "service")
	serviceRst    = desc("service_restarts_total", "Number of times the supervisor restarted the service.", "service")
	serviceState  = desc("service_state", "Current lifecycle state of the service (1 for the active state).", "service", "state")
)

var allDescs = []*prometheus.Desc{
	buildInfo, probeSuccess, lastProbe, batteryPct, batteryTemp, batteryChg, batteryPlug,
	wifiRSSI, wifiSpeed, wifiConnected, load, cpuUsage, memTotal, memAvail, diskTotal, diskFree,
	serviceUp, serviceHealth, serviceRst, serviceState,
}

var states = []supervisor.State{supervisor.StateStarting, supervisor.StateRunning, supervisor.StateBackoff, supervisor.StateStopped}

type collector struct {
	src      Source
	node     string
	version  string
	disabled map[string]bool
}

// NewRegistry returns a registry with edgephone metrics plus the standard Go
// runtime and process collectors.
func NewRegistry(src Source, node, version string, disabledProbes []string) *prometheus.Registry {
	d := map[string]bool{}
	for _, p := range disabledProbes {
		d[p] = true
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		&collector{src: src, node: node, version: version, disabled: d},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range allDescs {
		ch <- d
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	gauge(buildInfo, 1, c.version, runtime.GOOS, runtime.GOARCH, c.node)

	s := c.src.Snapshot()
	if !s.Time.IsZero() {
		gauge(lastProbe, float64(s.Time.UnixMilli())/1000)
		for _, name := range config.KnownProbes {
			if c.disabled[name] {
				continue
			}
			_, failed := s.Errors[name]
			gauge(probeSuccess, b2f(!failed), name)
		}
	}
	if b := s.Battery; b != nil {
		gauge(batteryPct, b.Percent)
		gauge(batteryTemp, b.TempC)
		gauge(batteryChg, b2f(b.Charging))
		gauge(batteryPlug, b2f(b.Plugged))
	}
	if w := s.Wifi; w != nil {
		gauge(wifiConnected, b2f(w.Connected))
		if w.Connected {
			gauge(wifiRSSI, w.RSSI)
			gauge(wifiSpeed, w.LinkSpeedMbps)
		}
	}
	if cpu := s.CPU; cpu != nil {
		gauge(load, cpu.Load1, "1m")
		gauge(load, cpu.Load5, "5m")
		gauge(load, cpu.Load15, "15m")
		if cpu.Usage != nil {
			gauge(cpuUsage, *cpu.Usage)
		}
	}
	if m := s.Memory; m != nil {
		gauge(memTotal, float64(m.TotalBytes))
		gauge(memAvail, float64(m.AvailableBytes))
	}
	if d := s.Disk; d != nil {
		gauge(diskTotal, float64(d.TotalBytes), d.Path)
		gauge(diskFree, float64(d.FreeBytes), d.Path)
	}

	for _, st := range c.src.Services() {
		gauge(serviceUp, b2f(st.Up()), st.Name)
		if st.Healthy != nil {
			gauge(serviceHealth, b2f(*st.Healthy), st.Name)
		}
		ch <- prometheus.MustNewConstMetric(serviceRst, prometheus.CounterValue, float64(st.Restarts), st.Name)
		for _, state := range states {
			gauge(serviceState, b2f(st.State == state), st.Name, string(state))
		}
	}
}
