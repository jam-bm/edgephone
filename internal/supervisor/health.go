package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"

	"github.com/jam-bm/edgephone/internal/config"
)

// Checker reports whether a service is healthy.
type Checker interface {
	Check(ctx context.Context) error
}

// CheckerFunc adapts a function to Checker.
type CheckerFunc func(ctx context.Context) error

// Check implements Checker.
func (f CheckerFunc) Check(ctx context.Context) error { return f(ctx) }

// NewChecker builds the checker described by h.
func NewChecker(h config.Health, workdir string) Checker {
	switch {
	case h.HTTP != "":
		return httpChecker{url: h.HTTP, client: &http.Client{}}
	case h.TCP != "":
		return tcpChecker{addr: h.TCP}
	default:
		return execChecker{argv: h.Exec, dir: workdir}
	}
}

type httpChecker struct {
	url    string
	client *http.Client
}

func (c httpChecker) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 399 {
		return fmt.Errorf("GET %s: %s", c.url, resp.Status)
	}
	return nil
}

type tcpChecker struct{ addr string }

func (c tcpChecker) Check(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

type execChecker struct {
	argv []string
	dir  string
}

func (c execChecker) Check(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	cmd.Dir = c.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		out = bytes.TrimSpace(out)
		if len(out) == 0 {
			return err
		}
		if len(out) > 200 {
			out = out[:200]
		}
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}
