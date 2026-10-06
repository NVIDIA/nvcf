/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// Package tarstream moves a directory tree between agents as an
// uncompressed tar: regular files, directories and relative symlinks,
// nothing that escapes the destination.
package tarstream

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Write streams dir as a tar of relative paths. Names in skip (relative
// to dir) are left out. Absolute symlinks and special files are skipped.
func Write(w io.Writer, dir string, skip map[string]bool) error {
	tw := tar.NewWriter(w)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || rel == "." {
			return rerr
		}
		if skip[rel] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, lerr := os.Readlink(p)
			if lerr != nil || filepath.IsAbs(target) {
				return nil
			}
			link = target
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(info, link)
		if herr != nil {
			return herr
		}
		// PAX keeps the modification time to the nanosecond. Ninja-based
		// JIT caches (FlashInfer cached_ops, sgl_kernel) decide reuse or
		// rebuild by comparing each output's mtime with the build time
		// recorded in .ninja_log; a copy stamped with the copy time, or
		// truncated to seconds, rebuilds the whole op on every start
		// (GB300, 2026-10-01: a 24 s Mamba kernel rebuilt on every warm
		// start although the set carried it).
		hdr.Format = tar.FormatPAX
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if werr := tw.WriteHeader(hdr); werr != nil {
			return werr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, ferr := os.Open(p)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		_, cerr := io.Copy(tw, f)
		return cerr
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Extract unpacks a tar stream into dest. With worldWritable, directories
// become 0777 and files 0666 (0777 when executable), for a cachedir read
// and written by whichever user the engine runs as; otherwise the archive
// modes are kept.
func Extract(r io.Reader, dest string, worldWritable bool) (files int, bytes int64, err error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, 0, err
	}
	tr := tar.NewReader(r)
	// Directory times are restored last: creating entries inside a
	// directory updates its mtime.
	type dirTime struct {
		path string
		at   time.Time
	}
	var dirs []dirTime
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			for i := len(dirs) - 1; i >= 0; i-- {
				_ = os.Chtimes(dirs[i].path, dirs[i].at, dirs[i].at)
			}
			return files, bytes, nil
		}
		if err != nil {
			return files, bytes, err
		}
		rel := filepath.Clean(hdr.Name)
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			continue
		}
		target := filepath.Join(dest, rel)
		// Each symlink passes the lexical check above on its own, but links
		// resolve through one another ("d -> .", then "d/f -> ../x"), so
		// no entry may be created beneath a symlink, and an entry replaces
		// a symlink at its own path rather than following it. Write never
		// emits an entry under a symlink, so one here is a corrupt stream.
		if err := realParents(dest, rel); err != nil {
			return files, bytes, err
		}
		if err := dropSymlink(target); err != nil {
			return files, bytes, err
		}
		mode := os.FileMode(hdr.Mode) & os.ModePerm
		switch hdr.Typeflag {
		case tar.TypeDir:
			if worldWritable {
				mode = 0o777
			}
			if err := os.MkdirAll(target, mode|0o700); err != nil {
				return files, bytes, err
			}
			_ = os.Chmod(target, mode)
			if !hdr.ModTime.IsZero() {
				dirs = append(dirs, dirTime{target, hdr.ModTime})
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, bytes, err
			}
			if worldWritable {
				mode = 0o666
				if hdr.FileInfo().Mode()&0o111 != 0 {
					mode = 0o777
				}
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, mode|0o600)
			if err != nil {
				return files, bytes, err
			}
			n, cerr := io.Copy(f, tr)
			f.Close()
			if cerr != nil {
				return files, bytes, cerr
			}
			_ = os.Chmod(target, mode)
			if !hdr.ModTime.IsZero() {
				_ = os.Chtimes(target, hdr.ModTime, hdr.ModTime)
			}
			files++
			bytes += n
		case tar.TypeSymlink:
			if filepath.IsAbs(hdr.Linkname) || strings.HasPrefix(filepath.Clean(filepath.Join(filepath.Dir(rel), hdr.Linkname)), "..") {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return files, bytes, err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return files, bytes, err
			}
		}
	}
}

// realParents fails when a directory component of rel below dest is a
// symlink. Components that do not exist yet are created as directories.
func realParents(dest, rel string) error {
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	cur := dest
	for _, part := range strings.Split(dir, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("tarstream: entry %s lies under symlink %s", rel, cur)
		}
		if !fi.IsDir() {
			return fmt.Errorf("tarstream: entry %s lies under non-directory %s", rel, cur)
		}
	}
	return nil
}

// dropSymlink removes a symlink at target, so the entry replaces it.
func dropSymlink(target string) error {
	fi, err := os.Lstat(target)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	return os.Remove(target)
}

// ExtractFresh unpacks a stream into an empty staging directory next to
// dest and replaces dest with it only when the whole stream extracted. A
// retried transfer therefore never merges into what an interrupted
// attempt left, and a failed one leaves dest as it was.
func ExtractFresh(r io.Reader, dest string, worldWritable bool) (files int, bytes int64, err error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, 0, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dest), filepath.Base(dest)+".partial-")
	if err != nil {
		return 0, 0, err
	}
	defer os.RemoveAll(staging)
	if files, bytes, err = Extract(r, staging, worldWritable); err != nil {
		return files, bytes, err
	}
	if worldWritable {
		_ = os.Chmod(staging, 0o777)
	} else {
		_ = os.Chmod(staging, 0o755)
	}
	if err := os.RemoveAll(dest); err != nil {
		return files, bytes, err
	}
	if err := os.Rename(staging, dest); err != nil {
		return files, bytes, err
	}
	return files, bytes, nil
}
