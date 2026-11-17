# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] (not yet released)

### Added

- `edgephone agent`: a supervisor with exponential backoff, HTTP/TCP/exec health checks,
  start periods, process-group shutdown (SIGTERM, then SIGKILL) and rotating per-service logs.
- Probes for battery and Wi-Fi (Termux:API), load/CPU/memory (`/proc`) and disk (`statfs`),
  each with its own `edgephone_probe_success` metric.
- Prometheus `/metrics`, JSON `/status` and `/healthz` endpoints.
- `edgephone status`, `edgephone logs [-f]`, `edgephone init`.
- `edgephone doctor` with checks for Termux, Termux:API, Termux:Boot, the wake lock, battery optimization
  (with Samsung-specific steps), the phantom process killer, storage and config.
- `install.sh` with SHA-256 verification, and a Termux:Boot autostart script.
- goreleaser builds for `android/arm64`, `linux/arm64` and `linux/amd64`.
- Support for the Google Play build of Termux: services are started through `linker64`, and `doctor`
  explains which features need the F-Droid build.

### Fixed before release (found on a real Galaxy A30)

- A service whose main process died while a child still held its log pipe was only noticed after
  `stop_timeout`. It is now restarted immediately and the child is killed.
- Exec health check errors no longer end with a stray `: ` when the check prints nothing.
