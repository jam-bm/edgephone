# Troubleshooting

Run `edgephone doctor` first. It checks most of what's below and prints the fix.

## The agent stops when the screen turns off

Android suspends the CPU and kills background apps. You need all three:

1. **Wake lock**: the boot script runs `termux-wake-lock`. The Termux notification should say
   "wake lock held".
2. **Battery optimization off for Termux**: Settings > Apps > Termux > Battery > Unrestricted.
3. **Vendor app killers**: Samsung, Xiaomi, Huawei and others add their own on top of Android.
   On Samsung (One UI), go to Settings > Battery and device care > Battery > Background usage limits,
   turn off "Put unused apps to sleep", and add Termux, Termux:API and Termux:Boot to "Never sleeping apps".
   For other brands, see <https://dontkillmyapp.com>.

## Services die with "signal 9" on Android 12+

That's the phantom process killer, which limits how many child processes apps may keep. Fix it from a computer:

```bash
adb shell device_config set_sync_disabled_for_tests persistent
adb shell device_config put activity_manager max_phantom_processes 2147483647
```

On Android 14+ you can instead enable Developer options > "Disable child process restrictions".

## Battery and Wi-Fi metrics are missing

- `edgephone_probe_success{probe="battery"} 0` with "not found": run `pkg install termux-api`.
- "timed out": the **Termux:API app** isn't installed, or it comes from a different source than Termux.
  Install both from F-Droid. Play Store and F-Droid builds are signed differently and can't talk to each other.
- The Wi-Fi SSID shows `<unknown ssid>`: Android hides it without location permission. RSSI and link speed still work.

## Load average looks very high

On many Android phones the load average sits far above the number of CPU cores even when the phone is idle
(18-20 on the reference Galaxy A30, which has 8 cores). Linux counts threads waiting on I/O or in vendor drivers
as "running", and Android kernels keep many of those. Watch how it changes over time, and use
`edgephone_cpu_usage_ratio` (when available) for actual CPU use.

## CPU usage is missing but load average works

This is expected on Android 8 and later, where SELinux blocks `/proc/stat` for apps. `edgephone_load_average` is
still reported.

## DNS lookups fail inside services or edgephone

Use the `android_arm64` build. Go's `linux` target looks for `/etc/resolv.conf`, which Android doesn't have.
For your own Go services, build with `GOOS=android GOARCH=arm64 CGO_ENABLED=0`.

## `bind: permission denied` on port 80 or 443

Ports below 1024 need root. Use 8080/8443 and put a reverse proxy or Tailscale Funnel in front if you need to.

## Where are the logs?

- Agent log, when started by Termux:Boot: `~/.local/state/edgephone/agent.log`
- Service logs: `~/.local/state/edgephone/logs/<service>.log`. Use `edgephone logs -f <service>`.
