package doctor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jam-bm/edgephone/internal/probe"
)

// fakePhone returns an Env that looks like a Galaxy A30 on Android 11.
func fakePhone(t *testing.T) (Env, map[string]string) {
	home := t.TempDir()
	out := map[string]string{
		"getprop ro.product.manufacturer":  "samsung",
		"getprop ro.product.model":         "SM-A305F",
		"getprop ro.build.version.sdk":     "30",
		"getprop ro.build.version.release": "11",
		"termux-battery-status":            `{"percentage": 81, "temperature": 30.2}`,
	}
	env := Env{
		Getenv:   func(k string) string { return map[string]string{"PREFIX": "/data/data/com.termux/files/usr"}[k] },
		LookPath: func(string) (string, error) { return "/data/data/com.termux/files/usr/bin/x", nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			if v, ok := out[key]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("unexpected command " + key)
		},
		ReadFile:   os.ReadFile,
		ReadDir:    os.ReadDir,
		Stat:       os.Stat,
		StatDisk:   func(string) (*probe.Disk, error) { return &probe.Disk{TotalBytes: 100 << 30, FreeBytes: 40 << 30}, nil },
		Home:       home,
		GOOS:       "android",
		ConfigPath: filepath.Join(home, ".config", "edgephone", "config.yaml"),
	}
	return env, out
}

func byName(results []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range results {
		m[r.Name] = r
	}
	return m
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestHealthyPhone(t *testing.T) {
	env, _ := fakePhone(t)
	writeFile(t, filepath.Join(env.Home, ".termux", "boot", "start-edgephone"),
		"#!/data/data/com.termux/files/usr/bin/sh\ntermux-wake-lock\nexec edgephone agent\n")
	writeFile(t, env.ConfigPath, "node: { name: galaxy-a30 }\n")

	results := Run(context.Background(), env)
	for _, r := range results {
		if r.Level > Manual {
			t.Errorf("%s: %s %s", r.Name, r.Level.tag(), r.Detail)
		}
	}
	got := byName(results)
	if !strings.Contains(got["Termux"].Detail, "samsung SM-A305F (Android 11)") {
		t.Errorf("termux detail = %q", got["Termux"].Detail)
	}
	if !strings.Contains(got["Battery optimization"].Fix, "Never sleeping apps") {
		t.Errorf("samsung phones should get Samsung-specific steps: %q", got["Battery optimization"].Fix)
	}
	if !strings.Contains(got["Termux:API"].Detail, "81%") {
		t.Errorf("termux api detail = %q", got["Termux:API"].Detail)
	}

	var buf bytes.Buffer
	if !Print(&buf, results) {
		t.Error("Print reported failure")
	}
}

func TestFreshPhone(t *testing.T) {
	env, out := fakePhone(t)
	env.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	out["getprop ro.build.version.sdk"] = "33"

	got := byName(Run(context.Background(), env))
	want := map[string]Level{
		"Termux:API":             Fail,
		"Termux:Boot":            Warn,
		"Wake lock":              Warn,
		"Phantom process killer": Warn,
		"Config":                 Warn,
	}
	for name, level := range want {
		if got[name].Level != level {
			t.Errorf("%s = %s (%s), want %s", name, got[name].Level.tag(), got[name].Detail, level.tag())
		}
		if got[name].Fix == "" {
			t.Errorf("%s has no fix", name)
		}
	}
	if !strings.Contains(got["Termux:API"].Fix, "pkg install termux-api") {
		t.Errorf("fix = %q", got["Termux:API"].Fix)
	}
}

func TestTermuxAPIAppMissing(t *testing.T) {
	env, out := fakePhone(t)
	delete(out, "termux-battery-status") // command exists but the app never answers
	r := byName(Run(context.Background(), env))["Termux:API"]
	if r.Level != Fail || !strings.Contains(r.Fix, "F-Droid") {
		t.Errorf("result = %+v", r)
	}
}

func TestLinuxBuildOnAndroidWarnsAboutDNS(t *testing.T) {
	env, _ := fakePhone(t)
	env.GOOS = "linux"
	r := byName(Run(context.Background(), env))["Binary"]
	if r.Level != Warn || !strings.Contains(r.Detail, "DNS") {
		t.Errorf("result = %+v", r)
	}
}

func TestLowDisk(t *testing.T) {
	env, _ := fakePhone(t)
	env.StatDisk = func(string) (*probe.Disk, error) { return &probe.Disk{TotalBytes: 100, FreeBytes: 3}, nil }
	if r := byName(Run(context.Background(), env))["Storage"]; r.Level != Fail {
		t.Errorf("result = %+v", r)
	}
}

func TestInvalidConfigFails(t *testing.T) {
	env, _ := fakePhone(t)
	writeFile(t, env.ConfigPath, "services: [{ name: api }]\n")
	r := byName(Run(context.Background(), env))["Config"]
	if r.Level != Fail || !strings.Contains(r.Detail, "cmd is required") {
		t.Errorf("result = %+v", r)
	}
}
