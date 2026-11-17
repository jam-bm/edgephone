package termux

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFixArgs(t *testing.T) {
	for _, tc := range []struct {
		in        []string
		viaLinker bool
		want      []string
	}{
		// Observed on a Galaxy A30 with Termux googleplay.2026.10.09.
		{[]string{"edgephone", "/data/data/com.termux/files/usr/bin/edgephone", "doctor"}, true, []string{"edgephone", "doctor"}},
		{[]string{"edgephone"}, true, []string{"edgephone"}},
		// Started directly: never touch the args.
		{[]string{"edgephone", "/data/data/com.termux/files/usr/bin/edgephone"}, false, []string{"edgephone", "/data/data/com.termux/files/usr/bin/edgephone"}},
		// A real argument that happens to be a path is kept.
		{[]string{"edgephone", "/tmp/config.yaml"}, true, []string{"edgephone", "/tmp/config.yaml"}},
	} {
		if got := fixArgs(tc.in, tc.viaLinker); !slices.Equal(got, tc.want) {
			t.Errorf("fixArgs(%q, %v) = %q, want %q", tc.in, tc.viaLinker, got, tc.want)
		}
	}
}

func TestWrap(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "usr")
	bin := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		p := filepath.Join(bin, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sh := write("sh", "\x7fELF fake binary")
	// Android has no /usr, so /usr/bin/x in a #! line means $PREFIX/bin/x. Use a
	// name no build host has in /usr/bin, or Linux CI would find its own copy.
	env := write("edgephone-fake-env", "\x7fELF fake binary")
	script := write("termux-battery-status", "#!"+sh+"\nexec termux-api BatteryStatus\n")
	envScript := write("tool", "#!/usr/bin/edgephone-fake-env  python3 -u\nprint()\n")
	const linker = "/system/bin/linker64"

	for _, tc := range []struct {
		name string
		path string
		args []string
		want []string
	}{
		{"elf", sh, []string{"-c", "true"}, []string{linker, sh, "-c", "true"}},
		{"script", script, []string{"-x"}, []string{linker, sh, script, "-x"}},
		{"env script maps to prefix", envScript, nil, []string{linker, env, "python3 -u", envScript}},
		{"system binary runs directly", "/system/bin/getprop", []string{"ro.x"}, []string{"/system/bin/getprop", "ro.x"}},
	} {
		got, err := wrap(linker, prefix, tc.path, tc.args)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestWrapInterpreterLoop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "loop")
	if err := os.WriteFile(p, []byte("#!"+p+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := wrap("/system/bin/linker64", "", p, nil); err == nil {
		t.Fatal("want error for a script that names itself as interpreter")
	}
}

func TestCommandContextDirect(t *testing.T) {
	if Linker != "" {
		t.Skip("running under the loader")
	}
	cmd := CommandContext(context.Background(), "some-tool", "a")
	if !slices.Equal(cmd.Args, []string{"some-tool", "a"}) {
		t.Errorf("args = %q", cmd.Args)
	}
}
