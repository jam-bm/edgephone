// Package termux adapts edgephone to Termux builds that cannot execute files
// from the app's data directory, such as the Google Play build.
//
// Android forbids apps that target API 29+ from executing files they wrote
// themselves. The Play build of Termux works around this with termux-exec, an
// LD_PRELOAD library that rewrites every exec of $PREFIX/bin/foo into
// `/system/bin/linker64 $PREFIX/bin/foo ...`. Go programs make system calls
// directly instead of going through libc, so the library never sees their
// execs and they fail with "permission denied". This package does the same
// rewrite for edgephone's own execs.
//
// It also undoes a side effect on edgephone itself: when started through the
// loader, os.Args gains the binary's path as an extra second element.
package termux

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Linker is the dynamic loader edgephone was started through, or "" when it
// was executed directly (F-Droid Termux, plain Linux, tests).
var Linker = detectLinker()

func detectLinker() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if b := filepath.Base(exe); b == "linker64" || b == "linker" {
		return exe
	}
	return ""
}

// Args returns os.Args without the extra path the loader inserts.
func Args() []string { return fixArgs(os.Args, Linker != "") }

func fixArgs(args []string, viaLinker bool) []string {
	// Android paths: always slash-separated, whatever the build host.
	if viaLinker && len(args) > 1 && strings.HasPrefix(args[1], "/") &&
		path.Base(args[1]) == path.Base(args[0]) {
		return append([]string{args[0]}, args[2:]...)
	}
	return args
}

// CommandContext is exec.CommandContext, routed through the loader when
// edgephone itself was started through it.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if Linker == "" {
		return exec.CommandContext(ctx, name, args...)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return exec.CommandContext(ctx, name, args...) // fails with the lookup error
	}
	argv, err := wrap(Linker, os.Getenv("PREFIX"), path, args)
	if err != nil {
		return exec.CommandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

// wrap returns the argv that runs path with args through linker. Scripts are
// resolved through their #! line (the loader only understands ELF files), and
// system binaries are run directly because they are executable anyway.
func wrap(linker, prefix, path string, args []string) ([]string, error) {
	for depth := 0; depth < 4; depth++ {
		if isSystem(path) {
			return append([]string{path}, args...), nil
		}
		interp, arg, ok, err := shebang(path)
		if err != nil {
			return nil, err
		}
		if !ok {
			return append([]string{linker, path}, args...), nil
		}
		// Like termux-exec: /bin/sh and /usr/bin/env in scripts mean the
		// Termux copies, since Android has no /usr.
		if prefix != "" && (strings.HasPrefix(interp, "/bin/") || strings.HasPrefix(interp, "/usr/bin/")) {
			if _, err := os.Stat(interp); err != nil {
				interp = filepath.Join(prefix, "bin", filepath.Base(interp))
			}
		}
		rest := []string{path}
		if arg != "" {
			rest = []string{arg, path}
		}
		path, args = interp, append(rest, args...)
	}
	return nil, errors.New("too many levels of #! interpreters")
}

func isSystem(path string) bool {
	for _, dir := range []string{"/system/", "/apex/", "/vendor/", "/product/"} {
		if strings.HasPrefix(path, dir) {
			return true
		}
	}
	return false
}

// shebang reads a "#!interp [arg]" line. Like Linux, everything after the
// interpreter is passed as a single argument.
func shebang(path string) (interp, arg string, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false, err
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 256).ReadString('\n')
	if err != nil && line == "" {
		return "", "", false, nil // empty file: let the loader report it
	}
	if !strings.HasPrefix(line, "#!") {
		return "", "", false, nil
	}
	line = strings.TrimSpace(line[2:])
	interp, arg, _ = strings.Cut(line, " ")
	if interp == "" {
		return "", "", false, nil
	}
	return interp, strings.TrimSpace(arg), true, nil
}
