# Contributing

Thanks for helping! edgephone is small on purpose. One binary, no root, and no surprises on a phone.

## Development

```bash
make test     # go test -race ./...
make lint     # gofmt, go vet, staticcheck
make android  # cross-compile dist/edgephone_android_arm64
```

You don't need a phone for most changes. Probes take a fake command runner and a fake `/proc`
directory, and the supervisor tests use the test binary as the supervised process.

## Guidelines

- **Keep it rootless.** Anything that needs root or adb belongs in `doctor` as a documented manual step.
- **Start programs with `termux.CommandContext`, never `exec.Command`.** On the Google Play build of Termux,
  `exec.Command` fails with "permission denied" for anything in `$PREFIX`.
- **Never block forever.** Every external command (especially `termux-*`) needs a timeout.
- **Fail loud, degrade gracefully.** A broken probe or service must show up in `/metrics` and
  `edgephone status` without taking anything else down.
- **Tests come with behavior changes.** Use fakes, not sleeps, wherever possible.
- **Device reports are gold.** If you run edgephone on a phone we haven't seen, open an issue with
  your `edgephone doctor` output, even if everything passes.

## Testing on a phone

With USB debugging on, you can run a build without installing anything permanently:

```bash
make android
adb push dist/edgephone_android_arm64 /data/local/tmp/edgephone
adb shell chmod 755 /data/local/tmp/edgephone
adb shell /data/local/tmp/edgephone doctor
```

`adb shell` runs as the shell user, which can read more than an app can (for example `/proc/loadavg`).
To see what Termux really gets, copy the binary into Termux (`cp /data/local/tmp/edgephone $PREFIX/bin/`) and
run it there. Inside Termux, `/system/bin/log -t EP "..."` writes to logcat, which you can read with
`adb logcat -s EP`. Delete `/data/local/tmp/edgephone` when you're done.

## Releasing

1. Run the device smoke test in [docs/release-checklist.md](docs/release-checklist.md).
2. Update `CHANGELOG.md`.
3. Tag and push the tag (`git tag v0.x.y && git push origin v0.x.y`). The release workflow runs goreleaser.
