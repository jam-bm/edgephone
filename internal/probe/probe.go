// Package probe collects phone telemetry without root: Termux:API JSON for
// battery and Wi-Fi, /proc for CPU and memory, statfs for disk.
//
// Every probe is independent. A probe that fails (Termux:API missing, /proc
// restricted by SELinux, ...) records an error and the rest keep working.
package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/jam-bm/edgephone/internal/termux"
)

// Runner executes a command and returns its stdout. It exists so tests can
// replace termux-api calls with canned output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs real commands.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := termux.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out (is the Termux:API app installed?)", name)
	}
	if err != nil {
		if msg := bytes.TrimSpace(stderr.Bytes()); len(msg) > 0 {
			return nil, fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// Battery is parsed from termux-battery-status.
type Battery struct {
	Percent  float64 `json:"percent"`
	TempC    float64 `json:"temp_c"`
	Charging bool    `json:"charging"` // status == CHARGING
	Plugged  bool    `json:"plugged"`  // connected to a charger, even if full
	Status   string  `json:"status"`
	Health   string  `json:"health"`
}

// Wifi is parsed from termux-wifi-connectioninfo.
type Wifi struct {
	RSSI          float64 `json:"rssi_dbm"`
	LinkSpeedMbps float64 `json:"link_speed_mbps"`
	SSID          string  `json:"ssid"`
	Connected     bool    `json:"connected"`
}

// CPU holds load averages and, when /proc/stat is readable, usage since the
// previous sample.
type CPU struct {
	Load1, Load5, Load15 float64
	// Usage is busy/total in [0,1]; nil on the first sample or when
	// /proc/stat is not readable (Android 8+ blocks it for most apps).
	Usage *float64 `json:"usage,omitempty"`
}

// Memory is parsed from /proc/meminfo.
type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

// Disk describes the filesystem holding Probes.DiskPath.
type Disk struct {
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"` // available to unprivileged users
}

// FreePercent returns free space as a percentage of total.
func (d Disk) FreePercent() float64 {
	if d.TotalBytes == 0 {
		return 0
	}
	return float64(d.FreeBytes) / float64(d.TotalBytes) * 100
}

// Snapshot is one round of probing. A nil field means that probe was
// disabled or failed; Errors says which.
type Snapshot struct {
	Time    time.Time         `json:"time"`
	Battery *Battery          `json:"battery,omitempty"`
	Wifi    *Wifi             `json:"wifi,omitempty"`
	CPU     *CPU              `json:"cpu,omitempty"`
	Memory  *Memory           `json:"memory,omitempty"`
	Disk    *Disk             `json:"disk,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
}

// Prober collects snapshots. The zero value is not usable; use New.
type Prober struct {
	Run      Runner
	ProcRoot string // "/proc" in production
	DiskPath string
	Timeout  time.Duration
	Disabled map[string]bool
	Now      func() time.Time

	mu      sync.Mutex
	prevCPU *cpuTimes
}

// New returns a Prober reading the real system.
func New(diskPath string, disabled []string) *Prober {
	d := map[string]bool{}
	for _, name := range disabled {
		d[name] = true
	}
	return &Prober{
		Run:      ExecRunner,
		ProcRoot: "/proc",
		DiskPath: diskPath,
		Timeout:  10 * time.Second,
		Disabled: d,
		Now:      time.Now,
	}
}

// Collect runs every enabled probe. It never returns an error; failures are
// reported per probe in Snapshot.Errors.
func (p *Prober) Collect(ctx context.Context) Snapshot {
	s := Snapshot{Time: p.Now(), Errors: map[string]string{}}
	try := func(name string, fn func() error) {
		if p.Disabled[name] {
			return
		}
		if err := fn(); err != nil {
			s.Errors[name] = err.Error()
		}
	}

	// The two termux-api calls are slow (~0.5-1s each), so run them in parallel.
	var wg sync.WaitGroup
	var bat *Battery
	var wifi *Wifi
	var batErr, wifiErr error
	if !p.Disabled["battery"] {
		wg.Add(1)
		go func() { defer wg.Done(); bat, batErr = p.battery(ctx) }()
	}
	if !p.Disabled["wifi"] {
		wg.Add(1)
		go func() { defer wg.Done(); wifi, wifiErr = p.wifi(ctx) }()
	}

	try("cpu", func() (err error) { s.CPU, err = p.cpu(); return })
	try("memory", func() (err error) { s.Memory, err = readMeminfo(p.ProcRoot + "/meminfo"); return })
	try("disk", func() (err error) { s.Disk, err = StatDisk(p.DiskPath); return })

	wg.Wait()
	try("battery", func() error { s.Battery = bat; return batErr })
	try("wifi", func() error { s.Wifi = wifi; return wifiErr })
	if len(s.Errors) == 0 {
		s.Errors = nil
	}
	return s
}

func (p *Prober) runJSON(ctx context.Context, v any, name string) error {
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	out, err := p.Run(ctx, name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s not found: run 'pkg install termux-api'", name)
		}
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("%s: invalid JSON: %w", name, err)
	}
	return nil
}

type termuxBattery struct {
	Health      string   `json:"health"`
	Percentage  *float64 `json:"percentage"`
	Plugged     string   `json:"plugged"`
	Status      string   `json:"status"`
	Temperature float64  `json:"temperature"`
}

func (p *Prober) battery(ctx context.Context) (*Battery, error) {
	var raw termuxBattery
	if err := p.runJSON(ctx, &raw, "termux-battery-status"); err != nil {
		return nil, err
	}
	if raw.Percentage == nil {
		return nil, errors.New("termux-battery-status: no percentage in output")
	}
	return &Battery{
		Percent:  *raw.Percentage,
		TempC:    raw.Temperature,
		Charging: raw.Status == "CHARGING",
		Plugged:  raw.Plugged != "" && raw.Plugged != "UNPLUGGED",
		Status:   raw.Status,
		Health:   raw.Health,
	}, nil
}

type termuxWifi struct {
	RSSI            float64 `json:"rssi"`
	LinkSpeedMbps   float64 `json:"link_speed_mbps"`
	SSID            string  `json:"ssid"`
	SupplicantState string  `json:"supplicant_state"`
}

func (p *Prober) wifi(ctx context.Context) (*Wifi, error) {
	var raw termuxWifi
	if err := p.runJSON(ctx, &raw, "termux-wifi-connectioninfo"); err != nil {
		return nil, err
	}
	return &Wifi{
		RSSI:          raw.RSSI,
		LinkSpeedMbps: raw.LinkSpeedMbps,
		SSID:          raw.SSID,
		Connected:     raw.SupplicantState == "COMPLETED",
	}, nil
}

func (p *Prober) cpu() (*CPU, error) {
	c, err := readLoadavg(p.ProcRoot + "/loadavg")
	if err != nil {
		return nil, err
	}
	// /proc/stat is optional: SELinux denies it to apps on Android 8+.
	if cur, err := readStat(p.ProcRoot + "/stat"); err == nil {
		p.mu.Lock()
		if p.prevCPU != nil {
			if u, ok := usage(*p.prevCPU, cur); ok {
				c.Usage = &u
			}
		}
		p.prevCPU = &cur
		p.mu.Unlock()
	}
	return c, nil
}
