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
	"syscall"
	"time"
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

// exists reports whether name exists, without following a symlink there.
func (d *confinedDir) exists(name string) bool {
	_, err := d.root.Lstat(name)
	return err == nil
}

// writeFileAtomic writes name with mode perm through a rename, so a reader
// never sees a partial file. The temporary file is created exclusively.
func (d *confinedDir) writeFileAtomic(name string, data []byte, perm fs.FileMode) error {
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
