# Architecture

`edgephone agent` is one process with three concurrent parts: the **supervisor**, the **probe loop** and
the **HTTP server**. They share no locks except the latest probe snapshot and each service's status,
and both are copied out on read.

```
            ┌─────────────── edgephone agent ───────────────┐
 config ──▶ │  probe loop ──▶ snapshot ◀── metrics collector│ ◀── GET /metrics
            │                     ▲                         │ ◀── GET /status
            │  supervisor ──▶ []Status ─────────┘           │
            │    ├─ service A: run → wait → backoff → run   │
            │    └─ service B: + health monitor goroutine   │
            └───────────────────────────────────────────────┘
```

## Supervisor

Each service runs in its own goroutine loop:

1. **Start** the process in a new process group, with stdout and stderr going to a `RotatingFile`.
2. **Monitor** health on a ticker if `health` is configured. Failures during `start_period` are recorded
   but don't count. After `failures` consecutive failures, the run context is cancelled with
   `errUnhealthy` as the cause.
3. **Wait** for exit. Cancellation (shutdown or unhealthy) uses `exec.Cmd.Cancel` to send SIGTERM to
   the whole group, and `exec.Cmd.WaitDelay` escalates to SIGKILL after `stop_timeout`. After the
   leader exits, the group is sent SIGKILL to clean up stragglers.
4. **Back off** for `backoff_initial × 2^n`, capped at `backoff_max`. `n` resets once a run lasts
   `stable_after`, so a service that crashes once a day restarts after 1 s instead of 2 min.

The supervisor writes its own events (`started pid…`, `exit status 1; restarting in 4s`,
`health check failed (2/3)…`) into the same log file as the service, so `edgephone logs api` shows
the whole story.

## Probes

`Prober.Collect` runs the two slow Termux:API calls in parallel (each takes about 0.5–1 s, because
they go through an Android broadcast) while the fast `/proc` and `statfs` reads run inline. Each probe
fails on its own: a failure stores a message in `Snapshot.Errors[name]` and sets that field to `nil`.

`/proc/stat` and `/proc/loadavg` depend on the app's SELinux domain. `adb shell` can read both, many
Termux installs can read only `/proc/loadavg`, and the Google Play build of Termux can read neither.
CPU usage is reported only when `/proc/stat` can be read; when `/proc/loadavg` can't, the whole `cpu`
probe reports failure and memory and disk carry on.

## Starting programs on Termux

The Google Play build of Termux targets a recent Android API level, so the app may not execute files it
wrote itself. Its `termux-exec` library (loaded with `LD_PRELOAD`) rewrites `exec` calls to run them
through `/system/bin/linker64` instead. Go makes system calls directly and never goes through that
library.

`internal/termux` does the same rewrite. When `os.Executable()` is the loader (which is how the Play build
started edgephone), it drops the extra argument the loader inserts into `os.Args`, and
`termux.CommandContext` runs ELF files as `linker64 <file> args…`. Scripts are resolved through their `#!`
line first, with `/bin/x` and `/usr/bin/x` mapped to `$PREFIX/bin/x`. Everywhere else it is plain
`exec.CommandContext`.

Service processes started this way are ordinary processes with the same PID, so process groups, signals
and `cleanupGroup` work unchanged.

## Metrics

The collector holds no state of its own. On every scrape it reads the latest snapshot and the service
statuses, and emits const metrics. A disabled probe emits nothing. A failed probe emits
`edgephone_probe_success{probe=…} 0` and no data series, so stale values are never exported as
current ones.

## Configuration

YAML is decoded with `KnownFields(true)`, so unknown keys are errors. Defaults are applied before
validation. `Validate` collects every problem and returns them joined, so you can fix the whole config
in one pass.
