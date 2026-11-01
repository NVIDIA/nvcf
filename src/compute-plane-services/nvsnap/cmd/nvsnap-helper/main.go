// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nvsnap-helper runs the small steps nvsnap adds to a workload's pod: wait
// for a shared model, write it, seed a cache, sweep a volume into the page
// cache. (Holding a container idle is cmd/nvsnap-idle, which must be
// single-threaded.) It is static and comes from the node's nvsnap bundle
// (the agent stages /criu-bundle there), mounted read-only, so a step runs
// in the workload's own image, needing no extra pull, yet depends on
// nothing in it: no shell, no coreutils, no particular architecture.
//
// Where a step runs a program of the workload (a chart's own download, the
// image's Hugging Face downloader), the helper executes it directly,
// without a shell, and finds it on the image's own PATH.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	code := 0
	switch cmd {
	case "skip":
		skipCmd(args)
		return
	case "wait":
		code, err = waitCmd(args)
	case "write":
		code, err = writeCmd(args)
	case "seed":
		seedCmd(args)
	case "prewarm":
		prewarmCmd(args)
	case "await-pid-floor":
		awaitPIDFloorCmd(args, "/proc/sys/kernel/ns_last_pid")
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nvsnap:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nvsnap-helper skip | wait | write | seed | prewarm | await-pid-floor [flags] [-- command...]")
	os.Exit(2)
}

// tryFlags collects repeated --try values: alternative program names, the
// first found on PATH runs.
type tryFlags []string

func (t *tryFlags) String() string     { return strings.Join(*t, ",") }
func (t *tryFlags) Set(v string) error { *t = append(*t, v); return nil }

// command resolves the program to run: the first --try found on PATH with
// args, else args as given. It returns nil when there is nothing to run.
func command(try []string, args []string) ([]string, error) {
	if len(try) == 0 {
		if len(args) == 0 {
			return nil, nil
		}
		return args, nil
	}
	for _, name := range try {
		if p, err := exec.LookPath(name); err == nil {
			return append([]string{p}, args...), nil
		}
	}
	return nil, fmt.Errorf("none of %s is on PATH", strings.Join(try, ", "))
}

// run executes argv with the helper's stdio and returns its exit code.
func run(argv []string) (int, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), fmt.Errorf("%s exited %d", argv[0], exit.ExitCode())
	}
	if err != nil {
		return 127, err
	}
	return 0, nil
}

// idle holds a restore placeholder until the restored process takes over:
// it reaps whatever is reparented to it, and every two seconds runs a
// short child, so a pid the agent reserves in the namespace shows up in
// it. SIGTERM ends it cleanly.
// skipCmd stands in for a step that does not run: it says why and exits 0.
func skipCmd(args []string) {
	fs := flag.NewFlagSet("skip", flag.ExitOnError)
	reason := fs.String("reason", "", "why the step does not run")
	_ = fs.Parse(args)
	fmt.Println("nvsnap: skipped:", *reason)
}

// waitCmd waits for a shared model's completion marker. On a recorded
// failure, or past the deadline, it falls back: with --in-place it runs
// the given download (none: it lets the engine download), else the
// landing is read-only here and it fails, so the pod is recreated on its
// own download.
func waitCmd(args []string) (int, error) {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	marker := fs.String("marker", "", "completion marker")
	failed := fs.String("failed", "", "failure marker")
	deadline := fs.Int("deadline", 1800, "seconds")
	inPlace := fs.Bool("in-place", false, "the landing is writable: fall back to the download here")
	var try tryFlags
	fs.Var(&try, "try", "program to run the fallback with, first found on PATH")
	_ = fs.Parse(args)
	if *marker == "" {
		return 2, errors.New("wait: --marker is required")
	}
	start := time.Now()
	reason := ""
	for {
		if exists(*marker) {
			fmt.Println("nvsnap: model complete, skipping download")
			return 0, nil
		}
		if *failed != "" && exists(*failed) {
			b, _ := os.ReadFile(*failed)
			reason = "shared download failed: " + strings.TrimSpace(string(b))
			break
		}
		if time.Since(start) >= time.Duration(*deadline)*time.Second {
			reason = "marker deadline passed"
			break
		}
		time.Sleep(5 * time.Second)
	}
	fmt.Println("nvsnap:", reason)
	if !*inPlace {
		fmt.Println("nvsnap: the shared model volume is read-only here; the pod is recreated on its own download")
		return 1, nil
	}
	argv, err := command(try, fs.Args())
	if err != nil || argv == nil {
		fmt.Println("nvsnap: no download step to fall back to; the engine downloads the model itself")
		return 0, nil
	}
	fmt.Println("nvsnap: downloading locally")
	return run(argv)
}

// writeCmd runs a download into dir, then records completion: the marker
// holds the bytes written, also reported as the termination message.
func writeCmd(args []string) (int, error) {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	marker := fs.String("marker", "", "completion marker")
	failed := fs.String("failed", "", "failure marker to clear")
	dir := fs.String("dir", "", "landing directory")
	writable := fs.Int("writable-wait", 0, "seconds to wait for the landing to become writable")
	var try tryFlags
	fs.Var(&try, "try", "program to run, first found on PATH")
	_ = fs.Parse(args)
	if *marker == "" || *dir == "" {
		return 2, errors.New("write: --marker and --dir are required")
	}
	if exists(*marker) {
		fmt.Println("nvsnap: model already complete")
		return 0, nil
	}
	for waited := 0; syscall.Access(*dir, 2) != nil; waited += 2 { // W_OK
		if waited >= *writable {
			fmt.Fprintf(os.Stderr, "nvsnap: landing %s is not writable\n", *dir)
			break
		}
		time.Sleep(2 * time.Second)
	}
	if *failed != "" {
		_ = os.Remove(*failed)
	}
	argv, err := command(try, fs.Args())
	if err != nil {
		return 127, err
	}
	if argv == nil {
		return 2, errors.New("write: no command to run")
	}
	if code, err := run(argv); err != nil {
		return code, err
	}
	syscall.Sync()
	n := treeBytes(*dir)
	size := strconv.FormatInt(n, 10)
	if err := os.WriteFile(*marker, []byte(size), 0o644); err != nil {
		return 1, fmt.Errorf("write marker: %w", err)
	}
	_ = os.WriteFile("/dev/termination-log", []byte(size), 0o644)
	return 0, nil
}

// treeBytes is the apparent size of the files under dir.
func treeBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// seedCmd copies a cache set into the pod's cachedir, then lists what it
// placed. With --keep-owners every entry, the cachedir included, gets the
// owner and full mode it has in the set, as the engine left it: a
// restored engine reopens these files, and CRIU refuses any whose mode
// differs from the dump. Without, for a set whose copies were made
// root's, the copy is opened to any user (it is the pod's scratch, not the
// shared volume). With --marker it first waits for the source to be
// brought to the node, and past --deadline copies nothing. It never fails
// the pod: a missing or broken source leaves the cache empty and the
// engine compiles as it would have anyway.
func seedCmd(args []string) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	from := fs.String("from", "", "source directory")
	to := fs.String("to", "", "destination directory")
	label := fs.String("label", "", "what is seeded, for the log")
	index := fs.String("index", "", "file under --to listing the seeded paths")
	keepOwners := fs.Bool("keep-owners", false, "give every entry its owner and full mode in --from instead of opening the copy to any user")
	marker := fs.String("marker", "", "wait for this file before copying: --from is being brought to the node")
	links := fs.String("links", "", "JSON manifest whose \"links\" (path under --to: target) are made after the copy")
	deadline := fs.Int("deadline", 900, "seconds to wait for --marker; past them nothing is copied")
	_ = fs.Parse(args)
	if *marker != "" && !awaitMarker(*marker, time.Duration(*deadline)*time.Second, 2*time.Second) {
		fmt.Printf("nvsnap: %s not ready after %ds; the engine starts without it\n", *label, *deadline)
		*from = ""
	}
	if fi, err := os.Stat(*from); *from != "" && err == nil && fi.IsDir() {
		if err := copyTree(*from, *to, *keepOwners); err != nil {
			fmt.Fprintln(os.Stderr, "nvsnap: seed:", err)
		}
	}
	_ = os.MkdirAll(*to, 0o777)
	if *from != "" && *links != "" {
		if err := makeLinks(*links, *to); err != nil {
			fmt.Fprintln(os.Stderr, "nvsnap: seed links:", err)
		}
	}
	if !*keepOwners {
		openToAll(*to)
	}
	files := listFiles(*to, *index)
	if *index != "" {
		_ = os.WriteFile(filepath.Join(*to, *index), []byte(strings.Join(files, "\n")+"\n"), 0o666)
	}
	fmt.Printf("nvsnap: seeded %s, %d files\n", *label, len(files))
}

// makeLinks makes the symbolic links manifest lists under dir: each path,
// confined to dir, pointing where it pointed in the capture. A link whose
// path is taken is left as it is.
func makeLinks(manifest, dir string) error {
	b, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var m struct {
		Links map[string]string `json:"links"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for rel, target := range m.Links {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if r, err := filepath.Rel(dir, p); err != nil || r == "." || strings.HasPrefix(r, "..") {
			return fmt.Errorf("link %q is outside %s", rel, dir)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			return err
		}
		if err := os.Symlink(target, p); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

// awaitMarker waits for marker, polling every poll, for at most deadline.
func awaitMarker(marker string, deadline, poll time.Duration) bool {
	start := time.Now()
	for {
		if exists(marker) {
			return true
		}
		if time.Since(start) >= deadline {
			return false
		}
		time.Sleep(poll)
	}
}

// copyTree copies src's contents into dst, keeping modes, times and
// symlinks; existing directories are merged into. With exact, dst and
// every entry get their source's owner and full mode, past the umask, and
// a failure to give them is reported.
func copyTree(src, dst string, exact bool) error {
	var first error
	keep := func(out string, info fs.FileInfo) {
		if err := keepOwnerMode(out, info, exact); err != nil && first == nil {
			first = err
		}
	}
	_ = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if exact && first == nil {
				first = err
			}
			return nil // best effort: skip what cannot be read
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir():
			_ = os.MkdirAll(out, info.Mode().Perm()|0o700)
			keep(out, info)
		case info.Mode()&fs.ModeSymlink != 0:
			if t, err := os.Readlink(p); err == nil {
				_ = os.Remove(out)
				_ = os.Symlink(t, out)
				keep(out, info)
			}
		case info.Mode().IsRegular():
			if err := copyFile(p, out, info); err != nil {
				fmt.Fprintln(os.Stderr, "nvsnap: seed:", err)
				if exact && first == nil {
					first = err
				}
				return nil
			}
			keep(out, info)
		}
		return nil
	})
	return first
}

// keepOwnerMode gives the copy its source's owner, as cp -a does; only
// root can. With exact it also gives the full mode, setuid, setgid and
// sticky included, and reports what it could not give.
func keepOwnerMode(p string, info fs.FileInfo, exact bool) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if os.Geteuid() == 0 {
		if err := os.Lchown(p, int(st.Uid), int(st.Gid)); err != nil && exact {
			return err
		}
	}
	if !exact || info.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	// After the chown, which clears setuid and setgid.
	return syscall.Chmod(p, st.Mode&0o7777)
}

func copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// openToAll gives every user read and write on the tree, and search on
// directories and files already executable by someone (chmod -R a+rwX).
func openToAll(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		m := info.Mode().Perm() | 0o666
		if d.IsDir() || info.Mode().Perm()&0o111 != 0 {
			m |= 0o111
		}
		_ = os.Chmod(p, m)
		return nil
	})
}

// listFiles returns the regular files under root, relative and sorted,
// without skip.
func listFiles(root, skip string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel != skip {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// prewarmCmd reads a volume once so its pages are in the node's cache
// before the engine loads them. Parallelism comes from byte ranges, not
// files: a model is a dozen files, a few of them most of the bytes.
// Symlinks are followed (a Hugging Face snapshot is read through its entry
// names), original/ and blobs/ are skipped. Best effort, never fails.
// NVSNAP_PREWARM=0 in the environment turns it off.
func prewarmCmd(args []string) {
	fs := flag.NewFlagSet("prewarm", flag.ExitOnError)
	root := fs.String("root", "", "directory to read")
	workers := fs.Int("workers", 8, "parallel readers")
	chunk := fs.Int64("chunk", 256<<20, "bytes per range of a large file")
	_ = fs.Parse(args)
	if os.Getenv("NVSNAP_PREWARM") == "0" {
		return
	}
	type rng struct {
		path     string
		off, len int64
	}
	work := make(chan rng, 64)
	var wg sync.WaitGroup
	for range max(*workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 16<<20)
			for r := range work {
				f, err := os.Open(r.path)
				if err != nil {
					continue
				}
				for off, end := r.off, r.off+r.len; off < end; {
					n := int64(len(buf))
					if end-off < n {
						n = end - off
					}
					k, err := f.ReadAt(buf[:n], off)
					off += int64(k)
					if err != nil {
						break
					}
				}
				_ = f.Close()
			}
		}()
	}
	seen := map[[2]uint64]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			p := filepath.Join(dir, e.Name())
			if e.Name() == "original" || e.Name() == "blobs" {
				continue
			}
			fi, err := os.Stat(p) // follows symlinks
			if err != nil {
				continue
			}
			st, _ := fi.Sys().(*syscall.Stat_t)
			if fi.IsDir() {
				if st != nil {
					k := [2]uint64{st.Dev, st.Ino}
					if seen[k] {
						continue // a symlink loop
					}
					seen[k] = true
				}
				walk(p)
				continue
			}
			if !fi.Mode().IsRegular() {
				continue
			}
			for off := int64(0); off < fi.Size(); off += *chunk {
				work <- rng{path: p, off: off, len: min(*chunk, fi.Size()-off)}
			}
		}
	}
	walk(*root)
	close(work)
	wg.Wait()
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
