// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// File access in directories a workload can write: its GPU chunk store and
// the fabric session directory in it, a checkpoint directory a restored
// workload has mounted. The agent works there through an os.Root opened on
// the directory, so no path it opens resolves outside it, reads only
// regular files of bounded size, and writes by create-exclusive and rename
// within the root.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// confinedDir is a directory the agent works in through an os.Root.
type confinedDir struct {
	root *os.Root
	// host is the directory's path on the agent's side, for logs.
	host string
}

// openConfinedDir opens dir, which must exist and be a directory.
func openConfinedDir(dir string) (*confinedDir, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &confinedDir{root: r, host: dir}, nil
}

func (d *confinedDir) Close() error { return d.root.Close() }

// openWorkloadDir opens dir, a path inside the filesystem of host pid
// (its mount namespace's root): no component, dir's included, resolves
// outside that root.
func openWorkloadDir(procBase string, pid int, dir string) (*confinedDir, error) {
	rootPath := path.Join(procBase, strconv.Itoa(pid), "root")
	top, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	rel := strings.Trim(dir, "/")
	if rel == "" {
		return &confinedDir{root: top, host: rootPath}, nil
	}
	defer func() { _ = top.Close() }()
	r, err := top.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	return &confinedDir{root: r, host: path.Join(rootPath, rel)}, nil
}

// readFile reads name, a regular file of at most limit bytes. A name that
// is not a regular file (a FIFO, a device, a directory) is refused without
// blocking on it.
func (d *confinedDir) readFile(name string, limit int64) ([]byte, error) {
	f, err := d.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path.Join(d.host, name))
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("%s: %d bytes, over the limit of %d", path.Join(d.host, name), st.Size(), limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: over the limit of %d bytes", path.Join(d.host, name), limit)
	}
	return b, nil
}

// readTail reads the last max bytes of name, a regular file, without
// blocking on it.
func (d *confinedDir) readTail(name string, maxBytes int64) ([]byte, error) {
	f, err := d.root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path.Join(d.host, name))
	}
	if off := st.Size() - maxBytes; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, maxBytes))
}

// exists reports whether name exists, without following a symlink there.
func (d *confinedDir) exists(name string) bool {
	_, err := d.root.Lstat(name)
	return err == nil
}

// writeFileAtomic writes name through a rename, so a reader never sees a
// partial file. The temporary file is created exclusively. The file is
// 0644: the tools in the workload read what the agent writes there.
func (d *confinedDir) writeFileAtomic(name string, data []byte) error {
	const perm fs.FileMode = 0o644
	dir, base := path.Split(name)
	var f *os.File
	var tmp string
	for i := 0; ; i++ {
		tmp = dir + "." + base + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(i)
		var err error
		f, err = d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, perm)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i == 9 {
			return err
		}
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = d.root.Remove(tmp)
		return err
	}
	// The mode is set on the open file, not by name: the umask narrowed it.
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = d.root.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = d.root.Remove(tmp)
		return err
	}
	if err := d.root.Rename(tmp, name); err != nil {
		_ = d.root.Remove(tmp)
		return err
	}
	return nil
}

// harvestInto moves what src holds into dst, a directory the agent owns:
// regular files, and the directories that hold them. Anything else (a
// symlink, a FIFO, a device) fails the harvest: a checkpoint holds files
// only. Each entry moves by renameat between directory descriptors (one
// name, never a path), or by copy when src and dst are on different
// filesystems, and is checked to be a regular file once in dst, where
// nothing but the agent writes.
func harvestInto(src *confinedDir, dst string) error {
	return harvestDir(src, ".", dst)
}

func harvestDir(src *confinedDir, rel, dst string) error {
	sd, err := src.root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = sd.Close() }()
	ents, err := sd.ReadDir(-1)
	if err != nil {
		return err
	}
	dd, err := os.OpenFile(dst, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = dd.Close() }()
	for _, e := range ents {
		name := e.Name()
		var st unix.Stat_t
		if err := unix.Fstatat(int(sd.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("%s: %w", path.Join(src.host, rel, name), err)
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			to := path.Join(dst, name)
			if err := unix.Mkdirat(int(dd.Fd()), name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("%s: %w", to, err)
			}
			if err := harvestDir(src, path.Join(rel, name), to); err != nil {
				return err
			}
			_ = unix.Unlinkat(int(sd.Fd()), name, unix.AT_REMOVEDIR)
		case unix.S_IFREG:
			if err := moveRegularAt(sd, dd, name); err != nil {
				return fmt.Errorf("%s: %w", path.Join(src.host, rel, name), err)
			}
		default:
			return fmt.Errorf("%s: not a regular file or a directory; a checkpoint holds files only", path.Join(src.host, rel, name))
		}
	}
	return nil
}

// moveRegularAt moves name from directory sd to directory dd.
func moveRegularAt(sd, dd *os.File, name string) error {
	err := unix.Renameat(int(sd.Fd()), name, int(dd.Fd()), name)
	if errors.Is(err, unix.EXDEV) {
		err = copyRegularAt(sd, dd, name)
	}
	if err != nil {
		return err
	}
	// The name in src may have changed between the check and the move.
	var st unix.Stat_t
	if err := unix.Fstatat(int(dd.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Unlinkat(int(dd.Fd()), name, 0)
		return errors.New("changed into something other than a regular file while it was moved")
	}
	return nil
}

// copyRegularAt copies name, a regular file, from directory sd into a new
// file in dd, and removes it from sd.
func copyRegularAt(sd, dd *os.File, name string) error {
	in, err := unix.Openat(int(sd.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	inf := os.NewFile(uintptr(in), name)
	defer func() { _ = inf.Close() }()
	st, err := inf.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	out, err := unix.Openat(int(dd.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(st.Mode().Perm()))
	if err != nil {
		return err
	}
	outf := os.NewFile(uintptr(out), name)
	if _, err := io.Copy(outf, inf); err != nil {
		_ = outf.Close()
		_ = unix.Unlinkat(int(dd.Fd()), name, 0)
		return err
	}
	if err := outf.Close(); err != nil {
		_ = unix.Unlinkat(int(dd.Fd()), name, 0)
		return err
	}
	return unix.Unlinkat(int(sd.Fd()), name, 0)
}
