package probe

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Representative termux-api output. Replace with a capture from a real device
// when one is available (see docs/release-checklist.md).
const batteryJSON = `{
  "present": true,
  "technology": "Li-ion",
  "health": "GOOD",
  "plugged": "PLUGGED_AC",
  "status": "CHARGING",
  "temperature": 31.4,
  "voltage": 4211,
  "current": 512000,
  "percentage": 78,
  "level": 78,
  "scale": 100
}`

const wifiJSON = `{
  "bssid": "02:00:00:00:00:00",
  "frequency_mhz": 5180,
  "ip": "192.168.1.42",
  "link_speed_mbps": 433,
  "rssi": -58,
  "ssid": "<unknown ssid>",
  "ssid_hidden": false,
  "supplicant_state": "COMPLETED"
}`

func fakeRunner(outputs map[string]string, errs map[string]error) Runner {
	return func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if err, ok := errs[name]; ok {
			return nil, err
		}
		return []byte(outputs[name]), nil
	}
}

func fakeProc(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const meminfo = `MemTotal:        2880000 kB
MemFree:          120000 kB
MemAvailable:    1024000 kB
Buffers:           10000 kB
`

func newTestProber(t *testing.T, run Runner, proc string) *Prober {
	p := New(t.TempDir(), nil)
	p.Run = run
	p.ProcRoot = proc
	p.Now = func() time.Time { return time.Unix(1700000000, 0) }
	return p
}

func TestCollectParsesTermuxAndProc(t *testing.T) {
	proc := fakeProc(t, map[string]string{
		"loadavg": "1.50 0.75 0.25 2/512 1234\n",
		"meminfo": meminfo,
	})
	p := newTestProber(t, fakeRunner(map[string]string{
		"termux-battery-status":      batteryJSON,
		"termux-wifi-connectioninfo": wifiJSON,
	}, nil), proc)
	p.Disabled["disk"] = true // statfs is platform specific; tested below

	s := p.Collect(context.Background())
	if len(s.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", s.Errors)
	}
	b := s.Battery
	if b.Percent != 78 || b.TempC != 31.4 || !b.Charging || !b.Plugged || b.Health != "GOOD" {
		t.Errorf("battery = %+v", b)
	}
	if s.Wifi.RSSI != -58 || s.Wifi.LinkSpeedMbps != 433 || !s.Wifi.Connected {
		t.Errorf("wifi = %+v", s.Wifi)
	}
	if s.CPU.Load1 != 1.5 || s.CPU.Load15 != 0.25 || s.CPU.Usage != nil {
		t.Errorf("cpu = %+v", s.CPU)
	}
	if s.Memory.TotalBytes != 2880000*1024 || s.Memory.AvailableBytes != 1024000*1024 {
		t.Errorf("memory = %+v", s.Memory)
	}
}

func TestBatteryFullButPluggedIsNotCharging(t *testing.T) {
	p := newTestProber(t, fakeRunner(map[string]string{
		"termux-battery-status": `{"percentage":100,"plugged":"PLUGGED_USB","status":"FULL","temperature":30}`,
	}, nil), t.TempDir())
	b, err := p.battery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b.Charging || !b.Plugged {
		t.Errorf("battery = %+v, want plugged but not charging", b)
	}
}

func TestFailedProbesAreIsolated(t *testing.T) {
	proc := fakeProc(t, map[string]string{"meminfo": meminfo}) // no loadavg
	p := newTestProber(t, fakeRunner(
		map[string]string{"termux-wifi-connectioninfo": wifiJSON},
		map[string]error{"termux-battery-status": exec.ErrNotFound},
	), proc)
	p.Disabled["disk"] = true

	s := p.Collect(context.Background())
	if s.Battery != nil || s.CPU != nil {
		t.Errorf("failed probes should be nil: battery=%v cpu=%v", s.Battery, s.CPU)
	}
	if s.Wifi == nil || s.Memory == nil {
		t.Errorf("working probes should still report: wifi=%v memory=%v", s.Wifi, s.Memory)
	}
	if !strings.Contains(s.Errors["battery"], "pkg install termux-api") {
		t.Errorf("battery error should explain the fix: %q", s.Errors["battery"])
	}
	if _, ok := s.Errors["cpu"]; !ok {
		t.Errorf("cpu error missing: %v", s.Errors)
	}
}

func TestInvalidJSON(t *testing.T) {
	p := newTestProber(t, fakeRunner(map[string]string{"termux-battery-status": "not json"}, nil), t.TempDir())
	if _, err := p.battery(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestDisabledProbesAreSkipped(t *testing.T) {
	called := false
	run := func(context.Context, string, ...string) ([]byte, error) {
		called = true
		return nil, errors.New("should not run")
	}
	p := newTestProber(t, run, t.TempDir())
	for _, name := range []string{"battery", "wifi", "cpu", "memory", "disk"} {
		p.Disabled[name] = true
	}
	s := p.Collect(context.Background())
	if called || s.Errors != nil {
		t.Errorf("called=%v errors=%v", called, s.Errors)
	}
}

func TestCPUUsageFromStatDeltas(t *testing.T) {
	proc := fakeProc(t, map[string]string{
		"loadavg": "0.10 0.20 0.30 1/100 1\n",
		//          user nice system idle iowait irq softirq
		"stat": "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3 4\n",
	})
	p := newTestProber(t, nil, proc)
	if c, err := p.cpu(); err != nil || c.Usage != nil {
		t.Fatalf("first sample: usage=%v err=%v", c.Usage, err)
	}
	// +100 busy, +100 idle  => 50% usage.
	os.WriteFile(filepath.Join(proc, "stat"), []byte("cpu  150 0 150 750 150 0 0 0 0 0\n"), 0o644)
	c, err := p.cpu()
	if err != nil {
		t.Fatal(err)
	}
	if c.Usage == nil || math.Abs(*c.Usage-0.5) > 1e-9 {
		t.Fatalf("usage = %v, want 0.5", c.Usage)
	}
}

func TestCPUWithoutStatStillReportsLoad(t *testing.T) {
	// Android 8+ denies /proc/stat; loadavg alone must be enough.
	proc := fakeProc(t, map[string]string{"loadavg": "2.00 1.00 0.50 1/1 1\n"})
	p := newTestProber(t, nil, proc)
	c, err := p.cpu()
	if err != nil || c.Load1 != 2 || c.Usage != nil {
		t.Fatalf("cpu=%+v err=%v", c, err)
	}
}

func TestDiskFreePercent(t *testing.T) {
	d := Disk{TotalBytes: 200, FreeBytes: 50}
	if d.FreePercent() != 25 {
		t.Errorf("FreePercent = %v", d.FreePercent())
	}
	if (Disk{}).FreePercent() != 0 {
		t.Error("zero disk should be 0%")
	}
}
