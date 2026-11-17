// Package doctor checks that a phone is set up to run edgephone reliably and
// says exactly how to fix what isn't.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jam-bm/edgephone/internal/config"
	"github.com/jam-bm/edgephone/internal/probe"
)

// Level is the outcome of a check.
type Level int

// Check outcomes, from best to worst.
const (
	OK     Level = iota
	Info         // nothing wrong, just worth knowing
	Manual       // can't be verified from inside Termux; the user must check
	Warn
	Fail
)

func (l Level) tag() string {
	return [...]string{"[ ok ]", "[info]", "[todo]", "[warn]", "[FAIL]"}[l]
}

// Result is one check's outcome.
type Result struct {
	Name   string
	Level  Level
	Detail string
	Fix    string
}

// Env is everything the checks touch, so tests can fake a phone.
type Env struct {
	Getenv     func(string) string
	LookPath   func(string) (string, error)
	Run        probe.Runner
	ReadFile   func(string) ([]byte, error)
	ReadDir    func(string) ([]fs.DirEntry, error)
	Stat       func(string) (fs.FileInfo, error)
	StatDisk   func(string) (*probe.Disk, error)
	Home       string
	GOOS       string
	ConfigPath string
}

// SystemEnv returns the real environment.
func SystemEnv(configPath string) Env {
	home, _ := os.UserHomeDir()
	return Env{
		Getenv:     os.Getenv,
		LookPath:   exec.LookPath,
		Run:        probe.ExecRunner,
		ReadFile:   os.ReadFile,
		ReadDir:    os.ReadDir,
		Stat:       os.Stat,
		StatDisk:   probe.StatDisk,
		Home:       home,
		GOOS:       runtime.GOOS,
		ConfigPath: configPath,
	}
}

// Run executes every check in order.
func Run(ctx context.Context, env Env) []Result {
	d := &doctor{env: env, ctx: ctx}
	d.props = map[string]string{}
	for _, p := range []string{"ro.product.manufacturer", "ro.product.model", "ro.build.version.sdk", "ro.build.version.release"} {
		d.props[p] = d.getprop(p)
	}
	return []Result{
		d.termux(),
		d.build(),
		d.termuxAPI(),
		d.bootScript(),
		d.wakeLock(),
		d.batteryOptimization(),
		d.phantomKiller(),
		d.disk(),
		d.config(),
	}
}

// Print writes a human-readable report and returns true if nothing failed.
func Print(w io.Writer, results []Result) bool {
	ok := true
	width := 0
	for _, r := range results {
		width = max(width, len(r.Name))
	}
	for _, r := range results {
		fmt.Fprintf(w, "%s %-*s  %s\n", r.Level.tag(), width, r.Name, r.Detail)
		if r.Fix != "" {
			for _, line := range strings.Split(r.Fix, "\n") {
				fmt.Fprintf(w, "       %*s  %s\n", width, "", line)
			}
		}
		if r.Level == Fail {
			ok = false
		}
	}
	return ok
}

type doctor struct {
	env   Env
	ctx   context.Context
	props map[string]string
	boot  *bootInfo
}

func (d *doctor) getprop(name string) string {
	ctx, cancel := context.WithTimeout(d.ctx, 3*time.Second)
	defer cancel()
	out, err := d.env.Run(ctx, "getprop", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (d *doctor) onAndroid() bool {
	return d.env.GOOS == "android" || d.props["ro.build.version.sdk"] != ""
}

func (d *doctor) termux() Result {
	r := Result{Name: "Termux"}
	prefix := d.env.Getenv("PREFIX")
	if strings.Contains(prefix, "/com.termux/") {
		r.Level, r.Detail = OK, "running inside Termux"
		if m := d.props["ro.product.model"]; m != "" {
			r.Detail += fmt.Sprintf(" on %s %s (Android %s)", d.props["ro.product.manufacturer"], m, d.props["ro.build.version.release"])
		}
		return r
	}
	r.Level, r.Detail = Warn, "not running inside Termux; phone checks below may not apply"
	r.Fix = "Install Termux from F-Droid (the Play Store build is outdated) and run edgephone there."
	return r
}

func (d *doctor) build() Result {
	r := Result{Name: "Binary"}
	switch {
	case d.env.GOOS == "android":
		r.Level, r.Detail = OK, "built for android/"+runtime.GOARCH
	case d.onAndroid():
		r.Level = Warn
		r.Detail = "this is a " + d.env.GOOS + " build running on Android; DNS lookups may fail (no /etc/resolv.conf)"
		r.Fix = "Use the android_arm64 release, or build with GOOS=android GOARCH=arm64 CGO_ENABLED=0."
	default:
		r.Level, r.Detail = Info, "not on Android ("+d.env.GOOS+"); fine for development"
	}
	return r
}

func (d *doctor) termuxAPI() Result {
	r := Result{Name: "Termux:API"}
	if _, err := d.env.LookPath("termux-battery-status"); err != nil {
		r.Level, r.Detail = Fail, "termux-api package not installed; battery and Wi-Fi metrics are disabled"
		r.Fix = "pkg install termux-api   (and install the Termux:API app from F-Droid)"
		return r
	}
	// The CLI blocks forever if the companion app is missing, hence the timeout.
	ctx, cancel := context.WithTimeout(d.ctx, 8*time.Second)
	defer cancel()
	out, err := d.env.Run(ctx, "termux-battery-status")
	var bat struct {
		Percentage  *float64 `json:"percentage"`
		Temperature float64  `json:"temperature"`
	}
	if err == nil {
		err = json.Unmarshal(out, &bat)
	}
	if err == nil && bat.Percentage == nil {
		err = errors.New("no percentage in output")
	}
	if err != nil {
		r.Level, r.Detail = Fail, "termux-battery-status did not answer: "+err.Error()
		r.Fix = "Install the Termux:API *app* from F-Droid (same source as Termux), then open it once.\n" +
			"Mixing F-Droid and Play Store builds does not work: they are signed differently."
		return r
	}
	r.Level = OK
	r.Detail = fmt.Sprintf("app responds (battery %.0f%%, %.1f°C)", *bat.Percentage, bat.Temperature)
	return r
}

type bootInfo struct {
	dirExists bool
	script    string // path of a boot script that starts edgephone
	content   string
}

func (d *doctor) loadBoot() *bootInfo {
	if d.boot != nil {
		return d.boot
	}
	b := &bootInfo{}
	dir := filepath.Join(d.env.Home, ".termux", "boot")
	entries, err := d.env.ReadDir(dir)
	if err == nil {
		b.dirExists = true
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			data, err := d.env.ReadFile(p)
			if err == nil && strings.Contains(string(data), "edgephone") {
				b.script, b.content = p, string(data)
				break
			}
		}
	}
	d.boot = b
	return b
}

const bootFix = "Install the Termux:Boot app from F-Droid and open it once, then:\n" +
	"  mkdir -p ~/.termux/boot && cp scripts/termux-boot/start-edgephone ~/.termux/boot/ && chmod +x ~/.termux/boot/start-edgephone\n" +
	"(install.sh does this for you.)"

func (d *doctor) bootScript() Result {
	r := Result{Name: "Termux:Boot"}
	b := d.loadBoot()
	switch {
	case b.script != "":
		r.Level, r.Detail = OK, "autostart script: "+b.script
	case b.dirExists:
		r.Level, r.Detail, r.Fix = Warn, "~/.termux/boot exists but no script there starts edgephone", bootFix
	default:
		r.Level, r.Detail, r.Fix = Warn, "no ~/.termux/boot directory; edgephone will not start after a reboot", bootFix
	}
	return r
}

func (d *doctor) wakeLock() Result {
	r := Result{Name: "Wake lock"}
	b := d.loadBoot()
	if b.script != "" && strings.Contains(b.content, "termux-wake-lock") {
		r.Level, r.Detail = OK, "boot script acquires a wake lock"
		return r
	}
	r.Level = Warn
	r.Detail = "no termux-wake-lock in the boot script; Android may suspend the CPU when the screen is off"
	r.Fix = "Add 'termux-wake-lock' to the boot script, or run it once now.\n" +
		"The Termux notification shows \"wake lock held\" when it works."
	return r
}

func (d *doctor) batteryOptimization() Result {
	r := Result{Name: "Battery optimization", Level: Manual}
	r.Detail = "can't be read from inside Termux; check it once by hand"
	if strings.EqualFold(d.props["ro.product.manufacturer"], "samsung") {
		r.Fix = "Samsung (One UI; menu names vary slightly by version):\n" +
			"  Settings > Apps > Termux > Battery > Unrestricted\n" +
			"  Settings > Battery and device care > Battery > Background usage limits:\n" +
			"    turn off \"Put unused apps to sleep\" and add Termux to \"Never sleeping apps\"\n" +
			"Do the same for Termux:API and Termux:Boot."
	} else {
		r.Fix = "Settings > Apps > Termux > Battery > Unrestricted (or \"Don't optimize\").\n" +
			"From a computer: adb shell dumpsys deviceidle whitelist +com.termux\n" +
			"See https://dontkillmyapp.com for vendor-specific steps."
	}
	return r
}

func (d *doctor) phantomKiller() Result {
	r := Result{Name: "Phantom process killer"}
	sdk, err := strconv.Atoi(d.props["ro.build.version.sdk"])
	if err != nil {
		r.Level, r.Detail = Info, "Android version unknown; skipped"
		return r
	}
	if sdk < 31 {
		r.Level, r.Detail = OK, fmt.Sprintf("Android SDK %d: not affected (added in Android 12)", sdk)
		return r
	}
	r.Level = Warn
	r.Detail = fmt.Sprintf("Android SDK %d may kill child processes of Termux (signal 9)", sdk)
	r.Fix = "Android 14+: Developer options > \"Disable child process restrictions\".\n" +
		"Android 12-13, from a computer:\n" +
		"  adb shell device_config set_sync_disabled_for_tests persistent\n" +
		"  adb shell device_config put activity_manager max_phantom_processes 2147483647"
	return r
}

func (d *doctor) disk() Result {
	r := Result{Name: "Storage"}
	disk, err := d.env.StatDisk(d.env.Home)
	if err != nil {
		r.Level, r.Detail = Info, "could not read free space: "+err.Error()
		return r
	}
	free := disk.FreePercent()
	r.Detail = fmt.Sprintf("%.1f GiB free of %.1f GiB (%.0f%%)", gib(disk.FreeBytes), gib(disk.TotalBytes), free)
	switch {
	case free < 5:
		r.Level, r.Fix = Fail, "Free up space: services and logs will start failing. Try: du -sh ~/* | sort -h"
	case free < 10:
		r.Level, r.Fix = Warn, "Less than 10% free. Android slows down and apps misbehave when storage is nearly full."
	default:
		r.Level = OK
	}
	return r
}

func (d *doctor) config() Result {
	r := Result{Name: "Config"}
	if _, err := d.env.Stat(d.env.ConfigPath); errors.Is(err, fs.ErrNotExist) {
		r.Level, r.Detail = Warn, "no config at "+d.env.ConfigPath
		r.Fix = "edgephone init   (writes a commented example)"
		return r
	}
	cfg, err := config.Load(d.env.ConfigPath)
	if err != nil {
		r.Level, r.Detail = Fail, err.Error()
		return r
	}
	r.Level = OK
	r.Detail = fmt.Sprintf("%s: node %q, %d service(s)", d.env.ConfigPath, cfg.Node.Name, len(cfg.Services))
	return r
}

func gib(b uint64) float64 { return float64(b) / (1 << 30) }
