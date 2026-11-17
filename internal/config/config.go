// Package config loads and validates the edgephone YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root of config.yaml.
type Config struct {
	Node     Node      `yaml:"node"`
	Services []Service `yaml:"services"`
	Metrics  Metrics   `yaml:"metrics"`
	Probes   Probes    `yaml:"probes"`
	Logs     Logs      `yaml:"logs"`
}

// Node identifies this phone.
type Node struct {
	Name string `yaml:"name"`
}

// Service is a long-running process managed by the supervisor.
type Service struct {
	Name    string            `yaml:"name"`
	Cmd     []string          `yaml:"cmd"`
	Workdir string            `yaml:"workdir"`
	Env     map[string]string `yaml:"env"`
	Health  *Health           `yaml:"health"`
	Restart Restart           `yaml:"restart"`
	// StopTimeout is how long the process gets after SIGTERM before SIGKILL.
	StopTimeout time.Duration `yaml:"stop_timeout"`
}

// Health describes how to check that a running service is healthy.
// Exactly one of HTTP, TCP or Exec must be set.
type Health struct {
	HTTP     string        `yaml:"http"`
	TCP      string        `yaml:"tcp"`
	Exec     []string      `yaml:"exec"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
	// Failures is the number of consecutive failed checks before a restart.
	Failures int `yaml:"failures"`
	// StartPeriod is a grace period after start during which failures are ignored.
	StartPeriod time.Duration `yaml:"start_period"`
}

// Restart controls exponential backoff between restarts.
type Restart struct {
	BackoffInitial time.Duration `yaml:"backoff_initial"`
	BackoffMax     time.Duration `yaml:"backoff_max"`
	// StableAfter resets the backoff once a process has run this long.
	StableAfter time.Duration `yaml:"stable_after"`
}

// Metrics configures the HTTP endpoint serving /metrics, /status and /healthz.
type Metrics struct {
	Listen string `yaml:"listen"`
}

// Probes configures phone telemetry collection.
type Probes struct {
	Interval time.Duration `yaml:"interval"`
	// Disabled lists probes to skip: battery, wifi, cpu, memory, disk.
	Disabled []string `yaml:"disabled"`
	// DiskPath is the filesystem reported by the disk probe. Defaults to $HOME.
	DiskPath string `yaml:"disk_path"`
}

// Logs configures per-service log capture and rotation.
type Logs struct {
	Dir       string `yaml:"dir"`
	MaxSizeMB int    `yaml:"max_size_mb"`
	MaxFiles  int    `yaml:"max_files"`
}

// KnownProbes lists the probe names accepted in probes.disabled.
var KnownProbes = []string{"battery", "wifi", "cpu", "memory", "disk"}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// DefaultPath returns the conventional config location.
func DefaultPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "edgephone", "config.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "edgephone", "config.yaml")
}

// Load reads, defaults and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes YAML strictly (unknown keys are errors), applies defaults
// and validates the result.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// yaml.v3 returns io.EOF for an empty document; treat it as all defaults.
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Node.Name == "" {
		if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
			c.Node.Name = h
		} else {
			c.Node.Name = "phone"
		}
	}
	if c.Metrics.Listen == "" {
		c.Metrics.Listen = "127.0.0.1:9100"
	}
	if c.Probes.Interval == 0 {
		c.Probes.Interval = 30 * time.Second
	}
	home, _ := os.UserHomeDir()
	if c.Probes.DiskPath == "" {
		c.Probes.DiskPath = home
	}
	c.Probes.DiskPath = ExpandHome(c.Probes.DiskPath)
	if c.Logs.Dir == "" {
		c.Logs.Dir = filepath.Join(home, ".local", "state", "edgephone", "logs")
	}
	c.Logs.Dir = ExpandHome(c.Logs.Dir)
	if c.Logs.MaxSizeMB == 0 {
		c.Logs.MaxSizeMB = 10
	}
	if c.Logs.MaxFiles == 0 {
		c.Logs.MaxFiles = 3
	}
	for i := range c.Services {
		s := &c.Services[i]
		s.Workdir = ExpandHome(s.Workdir)
		if s.StopTimeout == 0 {
			s.StopTimeout = 10 * time.Second
		}
		if s.Restart.BackoffInitial == 0 {
			s.Restart.BackoffInitial = time.Second
		}
		if s.Restart.BackoffMax == 0 {
			s.Restart.BackoffMax = 2 * time.Minute
		}
		if s.Restart.StableAfter == 0 {
			s.Restart.StableAfter = time.Minute
		}
		if h := s.Health; h != nil {
			if h.Interval == 0 {
				h.Interval = 15 * time.Second
			}
			if h.Timeout == 0 {
				h.Timeout = 5 * time.Second
			}
			if h.Failures == 0 {
				h.Failures = 3
			}
		}
	}
}

// Validate reports every problem found, joined into one error.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	seen := map[string]bool{}
	for i, s := range c.Services {
		where := fmt.Sprintf("services[%d]", i)
		if s.Name != "" {
			where = fmt.Sprintf("service %q", s.Name)
		}
		switch {
		case s.Name == "":
			add("%s: name is required", where)
		case !serviceName.MatchString(s.Name):
			add("%s: name may only contain letters, digits, '.', '_' and '-'", where)
		case seen[s.Name]:
			add("%s: duplicate name", where)
		}
		seen[s.Name] = true
		if len(s.Cmd) == 0 || s.Cmd[0] == "" {
			add("%s: cmd is required", where)
		}
		if s.Restart.BackoffInitial < 0 || s.Restart.BackoffMax < s.Restart.BackoffInitial {
			add("%s: restart.backoff_max must be >= backoff_initial", where)
		}
		if h := s.Health; h != nil {
			n := 0
			if h.HTTP != "" {
				n++
				if !strings.HasPrefix(h.HTTP, "http://") && !strings.HasPrefix(h.HTTP, "https://") {
					add("%s: health.http must be an http:// or https:// URL", where)
				}
			}
			if h.TCP != "" {
				n++
			}
			if len(h.Exec) > 0 {
				n++
			}
			if n != 1 {
				add("%s: health needs exactly one of http, tcp or exec", where)
			}
			if h.Interval < 100*time.Millisecond {
				add("%s: health.interval is too short", where)
			}
			if h.Failures < 1 {
				add("%s: health.failures must be >= 1", where)
			}
		}
	}

	if c.Probes.Interval < time.Second {
		add("probes.interval must be at least 1s")
	}
	for _, p := range c.Probes.Disabled {
		if !contains(KnownProbes, p) {
			add("probes.disabled: unknown probe %q (known: %s)", p, strings.Join(KnownProbes, ", "))
		}
	}
	if c.Logs.MaxSizeMB < 1 || c.Logs.MaxFiles < 1 {
		add("logs.max_size_mb and logs.max_files must be >= 1")
	}
	return errors.Join(errs...)
}

// ExpandHome replaces a leading "~" with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
