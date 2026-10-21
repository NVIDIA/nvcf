// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// fakeProcFDs makes procBase/<pid>/fd/<n> -> target for each entry.
func fakeProcFDs(t *testing.T, pid int, fds map[int]string) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, strconv.Itoa(pid), "fd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, target := range fds {
		if err := os.Symlink(target, filepath.Join(dir, strconv.Itoa(n))); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func TestReadStdio(t *testing.T) {
	base := fakeProcFDs(t, 61, map[int]string{1: "pipe:[154672122]", 2: "pipe:[154672123]"})
	if got, want := readStdio(base, 61), (stdioPipes{Stdout: 154672122, Stderr: 154672123}); got != want {
		t.Fatalf("readStdio = %+v, want %+v", got, want)
	}
	// Not pipes (a terminal, a file): nothing to hand over.
	base = fakeProcFDs(t, 61, map[int]string{1: "/dev/pts/0", 2: "/var/log/x"})
	if got := readStdio(base, 61); got != (stdioPipes{}) {
		t.Fatalf("readStdio of non-pipes = %+v, want none", got)
	}
}

func TestStdioMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// A checkpoint taken before the pipes were recorded has no marker.
	if p, err := readStdioMarker(dir); err != nil || p != (stdioPipes{}) {
		t.Fatalf("missing marker = %+v, %v; want none, nil", p, err)
	}
	// Nothing to record: no marker.
	if err := writeStdioMarker(dir, stdioPipes{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, stdioMarkerFile)); !os.IsNotExist(err) {
		t.Fatalf("empty pipes wrote a marker (stat: %v)", err)
	}
	want := stdioPipes{Stdout: 7, Stderr: 8}
	if err := writeStdioMarker(dir, want); err != nil {
		t.Fatal(err)
	}
	if got, err := readStdioMarker(dir); err != nil || got != want {
		t.Fatalf("readStdioMarker = %+v, %v; want %+v", got, err, want)
	}
	if err := os.WriteFile(filepath.Join(dir, stdioMarkerFile), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readStdioMarker(dir); err == nil {
		t.Fatal("a corrupt marker read without error")
	}
}

func TestPlaceholderStdio(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	errf := filepath.Join(t.TempDir(), "err")
	for _, f := range []string{out, errf} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := fakeProcFDs(t, 100062, map[int]string{1: out, 2: errf})

	t.Run("both", func(t *testing.T) {
		files, args, err := placeholderStdio(base, 100062, stdioPipes{Stdout: 11, Stderr: 12})
		if err != nil {
			t.Fatal(err)
		}
		defer closeFiles(files)
		want := []string{"--inherit-fd", "fd[3]:pipe:[11]", "--inherit-fd", "fd[4]:pipe:[12]"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args = %q, want %q", args, want)
		}
		// fd 3 is the placeholder's stdout, fd 4 its stderr.
		if len(files) != 2 || files[0].Name() != filepath.Join(base, "100062", "fd", "1") || files[1].Name() != filepath.Join(base, "100062", "fd", "2") {
			t.Fatalf("files = %v", names(files))
		}
	})
	t.Run("one pipe for both", func(t *testing.T) {
		files, args, err := placeholderStdio(base, 100062, stdioPipes{Stdout: 11, Stderr: 11})
		if err != nil {
			t.Fatal(err)
		}
		defer closeFiles(files)
		if want := []string{"--inherit-fd", "fd[3]:pipe:[11]"}; !reflect.DeepEqual(args, want) || len(files) != 1 {
			t.Fatalf("args = %q (files %d), want %q", args, len(files), want)
		}
	})
	t.Run("stderr only", func(t *testing.T) {
		files, args, err := placeholderStdio(base, 100062, stdioPipes{Stderr: 12})
		if err != nil {
			t.Fatal(err)
		}
		defer closeFiles(files)
		if want := []string{"--inherit-fd", "fd[3]:pipe:[12]"}; !reflect.DeepEqual(args, want) || files[0].Name() != filepath.Join(base, "100062", "fd", "2") {
			t.Fatalf("args = %q files = %v", args, names(files))
		}
	})
	t.Run("none recorded", func(t *testing.T) {
		files, args, err := placeholderStdio(base, 100062, stdioPipes{})
		if err != nil || files != nil || args != nil {
			t.Fatalf("got %v %q %v; want nothing", names(files), args, err)
		}
	})
	t.Run("placeholder without stderr", func(t *testing.T) {
		base := fakeProcFDs(t, 5, map[int]string{1: out})
		files, args, err := placeholderStdio(base, 5, stdioPipes{Stdout: 11, Stderr: 12})
		if err == nil || files != nil || args != nil {
			t.Fatalf("got %v %q %v; want an error and nothing open", names(files), args, err)
		}
	})
}

// The restore reopens the placeholder's stdout through /proc: a pipe opened
// that way is the same pipe, so what the restored workload writes reaches
// whoever reads the placeholder's (the container runtime).
func TestPlaceholderStdioReachesThePipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	// Make the pipe this process's "fd 1" in a fake procfs, pointing at the
	// real /proc entry of its write end.
	base := fakeProcFDs(t, 1, map[int]string{1: "/proc/self/fd/" + strconv.Itoa(int(w.Fd()))})
	files, _, err := placeholderStdio(base, 1, stdioPipes{Stdout: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := files[0].WriteString("restored\n"); werr != nil {
		t.Fatal(werr)
	}
	closeFiles(files)
	_ = w.Close()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "restored\n" {
		t.Fatalf("read %q, %v; want %q", got, err, "restored\n")
	}
}

func closeFiles(fs []*os.File) {
	for _, f := range fs {
		_ = f.Close()
	}
}

func names(fs []*os.File) []string {
	n := make([]string, 0, len(fs))
	for _, f := range fs {
		n = append(n, f.Name())
	}
	return n
}
