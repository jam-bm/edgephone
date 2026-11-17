// Command edgephone turns an old Android phone into a monitored,
// self-healing server.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jam-bm/edgephone/internal/agent"
	"github.com/jam-bm/edgephone/internal/config"
	"github.com/jam-bm/edgephone/internal/doctor"
	"github.com/jam-bm/edgephone/internal/termux"
)

// Set by goreleaser: -ldflags "-X main.version=..."
var version = "dev"

//go:embed example.yaml
var exampleConfig []byte

const usage = `edgephone: run an old Android phone as a monitored, self-healing server.

Usage:
  edgephone <command> [flags]

Commands:
  agent     run the supervisor, probes and /metrics endpoint
  status    show phone telemetry and service state from a running agent
  logs      print (or follow) a service's log
  doctor    check that this phone is set up to run edgephone reliably
  init      write an example config file
  version   print the version

Run 'edgephone <command> -h' for command flags.
`

func main() {
	args := termux.Args()
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := args[1], args[2:]
	var err error
	switch cmd {
	case "agent":
		err = runAgent(args)
	case "status":
		err = runStatus(args)
	case "logs":
		err = runLogs(args)
	case "doctor":
		err = runDoctor(args)
	case "init":
		err = runInit(args)
	case "version", "--version", "-v":
		fmt.Printf("edgephone %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "edgephone: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		var ec exitCode
		if errors.As(err, &ec) {
			os.Exit(int(ec))
		}
		fmt.Fprintln(os.Stderr, "edgephone:", err)
		os.Exit(1)
	}
}

// exitCode lets a command exit non-zero without printing an error.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }

func newFlags(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: edgephone %s [flags] %s\n\nFlags:\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

func runAgent(args []string) error {
	fs := newFlags("agent", "")
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	logLevel := fs.String("log-level", "info", "debug, info, warn or error")
	logJSON := fs.Bool("log-json", false, "log as JSON instead of text")
	fs.Parse(args)

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("invalid -log-level %q", *logLevel)
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if *logJSON {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	logger := slog.New(h)

	cfg, err := config.Load(*cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no config at %s; create one with 'edgephone init'", *cfgPath)
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.New(cfg, version, logger, nil).Run(ctx)
}

// statusAddr turns a listen address like ":9100" into one we can dial.
func statusAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func runStatus(args []string) error {
	fs := newFlags("status", "")
	cfgPath := fs.String("config", config.DefaultPath(), "config file (used to find the agent's address)")
	addr := fs.String("addr", "", "agent address (default: metrics.listen from the config)")
	asJSON := fs.Bool("json", false, "print raw JSON")
	fs.Parse(args)

	if *addr == "" {
		*addr = "127.0.0.1:9100"
		if cfg, err := config.Load(*cfgPath); err == nil {
			*addr = statusAddr(cfg.Metrics.Listen)
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + *addr + "/status")
	if err != nil {
		return fmt.Errorf("agent not reachable at %s (is 'edgephone agent' running?): %w", *addr, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent returned %s", resp.Status)
	}
	if *asJSON {
		_, err = os.Stdout.Write(body)
		return err
	}
	var rep agent.StatusReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return err
	}
	printStatus(os.Stdout, rep, time.Now())
	return nil
}

func printStatus(w io.Writer, r agent.StatusReport, now time.Time) {
	fmt.Fprintf(w, "%s  edgephone %s  up %s\n\n", r.Node, r.Version, ago(now, r.StartedAt))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	s := r.Snapshot
	if b := s.Battery; b != nil {
		state := "on battery"
		switch {
		case b.Charging:
			state = "charging"
		case b.Plugged:
			state = "plugged in, not charging"
		}
		fmt.Fprintf(tw, "battery\t%.0f%%  %.1f°C  %s\n", b.Percent, b.TempC, state)
	}
	if wf := s.Wifi; wf != nil {
		if wf.Connected {
			fmt.Fprintf(tw, "wifi\t%.0f dBm  %.0f Mbit/s\n", wf.RSSI, wf.LinkSpeedMbps)
		} else {
			fmt.Fprintf(tw, "wifi\tdisconnected\n")
		}
	}
	if c := s.CPU; c != nil {
		line := fmt.Sprintf("load %.2f %.2f %.2f", c.Load1, c.Load5, c.Load15)
		if c.Usage != nil {
			line += fmt.Sprintf("  usage %.0f%%", *c.Usage*100)
		}
		fmt.Fprintf(tw, "cpu\t%s\n", line)
	}
	if m := s.Memory; m != nil {
		fmt.Fprintf(tw, "memory\t%s available of %s\n", humanBytes(m.AvailableBytes), humanBytes(m.TotalBytes))
	}
	if d := s.Disk; d != nil {
		fmt.Fprintf(tw, "disk\t%s free of %s (%.0f%%)\n", humanBytes(d.FreeBytes), humanBytes(d.TotalBytes), d.FreePercent())
	}
	for _, name := range slices.Sorted(maps.Keys(s.Errors)) {
		fmt.Fprintf(tw, "%s\tunavailable: %s\n", name, s.Errors[name])
	}
	tw.Flush()

	if len(r.Services) == 0 {
		fmt.Fprintln(w, "\nno services configured")
		return
	}
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tSTATE\tHEALTH\tPID\tRESTARTS\tFOR\tLAST EXIT")
	for _, st := range r.Services {
		health := "-"
		if st.Healthy != nil {
			health = map[bool]string{true: "ok", false: "failing"}[*st.Healthy]
		} else if st.HasHealth {
			health = "pending"
		}
		pid := "-"
		if st.PID != 0 {
			pid = fmt.Sprint(st.PID)
		}
		last := st.LastExit
		if last == "" {
			last = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", st.Name, st.State, health, pid, st.Restarts, ago(now, st.Since), last)
	}
	tw.Flush()
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func runLogs(args []string) error {
	fs := newFlags("logs", "<service>")
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	lines := fs.Int("n", 50, "number of lines to show")
	follow := fs.Bool("f", false, "keep printing new lines")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return exitCode(2)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	name := fs.Arg(0)
	found := false
	var names []string
	for _, s := range cfg.Services {
		names = append(names, s.Name)
		found = found || s.Name == name
	}
	if !found {
		return fmt.Errorf("no service %q in config (have: %s)", name, strings.Join(names, ", "))
	}
	path := filepath.Join(cfg.Logs.Dir, name+".log")
	offset, err := tail(os.Stdout, path, *lines)
	if err != nil {
		return err
	}
	if !*follow {
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return followFile(ctx, os.Stdout, path, offset)
}

// tail prints the last n lines of path and returns the file size.
func tail(w io.Writer, path string, n int) (int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil // service hasn't written anything yet
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	const maxRead = 1 << 20
	start := max(st.Size()-maxRead, 0)
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return 0, err
	}
	text := strings.TrimSuffix(string(buf), "\n")
	all := strings.Split(text, "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	if text != "" {
		fmt.Fprintln(w, strings.Join(all, "\n"))
	}
	return st.Size(), nil
}

func followFile(ctx context.Context, w io.Writer, path string, offset int64) error {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		if st.Size() < offset {
			offset = 0 // rotated
		}
		if st.Size() == offset {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		f.Seek(offset, io.SeekStart)
		n, _ := io.Copy(w, f)
		f.Close()
		offset += n
	}
}

func runDoctor(args []string) error {
	fs := newFlags("doctor", "")
	cfgPath := fs.String("config", config.DefaultPath(), "config file to validate")
	fs.Parse(args)

	fmt.Printf("edgephone %s doctor\n\n", version)
	results := doctor.Run(context.Background(), doctor.SystemEnv(*cfgPath))
	if !doctor.Print(os.Stdout, results) {
		fmt.Println("\nSome checks failed. Fix them and run 'edgephone doctor' again.")
		return exitCode(1)
	}
	fmt.Println("\nNo blocking problems found.")
	return nil
}

func runInit(args []string) error {
	fs := newFlags("init", "")
	cfgPath := fs.String("config", config.DefaultPath(), "where to write the config")
	force := fs.Bool("force", false, "overwrite an existing file")
	fs.Parse(args)

	if _, err := os.Stat(*cfgPath); err == nil && !*force {
		return fmt.Errorf("%s already exists (use -force to overwrite)", *cfgPath)
	}
	if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*cfgPath, exampleConfig, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nedit it, then run: edgephone doctor && edgephone agent\n", *cfgPath)
	return nil
}
