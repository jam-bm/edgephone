# Release checklist

CI covers logic. This checklist covers the things only a real phone can show.
Record the device and Android version in the release notes.

Reference device: Samsung Galaxy A30 (SM-A305F), Android 11, Termux from F-Droid.

## Install

- [ ] `curl -fsSL …/install.sh | bash` succeeds on a clean Termux and verifies the checksum
- [ ] `edgephone version` shows the tag and `android/arm64`

## Doctor

- [ ] `edgephone doctor` reports the correct manufacturer, model and Android version
- [ ] With the Termux:API app uninstalled, the Termux:API check fails within about 8 s (no hang)
- [ ] With everything set up, there are no `[FAIL]` lines

## Agent

- [ ] `curl 127.0.0.1:9100/metrics` shows battery, Wi-Fi, load, memory and disk with plausible values
- [ ] Plugging and unplugging the charger flips `edgephone_battery_plugged` within one probe interval
- [ ] A service with `cmd: ["sh", "-c", "exit 1"]` shows growing backoff in `edgephone logs`
- [ ] Killing a healthy service with `kill -9` makes it restart, and `restarts` goes up in `edgephone status`
- [ ] A service that spawns a child (`sh -c 'sleep 1000 & wait'`) leaves no orphan after `Ctrl-C` on the agent

## Survival

- [ ] Reboot the phone: the agent is running within about 1 min without opening Termux
- [ ] Screen off for 1 h on battery: `edgephone_last_probe_timestamp_seconds` has no gaps longer than 2× the interval
- [ ] Turn Wi-Fi off and on: `edgephone_wifi_connected` goes to 0, then back to 1, and services keep running

## Publish

- [ ] Update `CHANGELOG.md`
- [ ] `git tag vX.Y.Z && git push origin vX.Y.Z`
- [ ] Check that the release has 3 archives, `checksums.txt` and `install.sh`
