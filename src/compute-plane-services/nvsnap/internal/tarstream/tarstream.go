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
	"io"
	"os"
	"path/filepath"
	"strings"
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
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
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
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o600)
			if err != nil {
				return files, bytes, err
			}
			n, cerr := io.Copy(f, tr)
			f.Close()
			if cerr != nil {
				return files, bytes, cerr
			}
			_ = os.Chmod(target, mode)
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
