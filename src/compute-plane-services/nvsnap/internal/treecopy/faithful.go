// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package treecopy

// Faithful mode reproduces a stored tree exactly, as `tar --numeric-owner`
// would: device nodes (overlay whiteouts among them) and FIFOs are made
// again, and every entry keeps its owner and full mode, setuid, setgid and
// sticky bits included. The default suits copying a live root filesystem,
// where none of those belong in the copy.
//
// Direct reads bypass the page cache (O_DIRECT): reading a network volume
// through it tops out near 2 GB/s per node, while direct reads run several
// times that with enough of them in flight. Writes still go through the
// page cache, where whoever reads the copy next finds it.

import (
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	directIOAlign   = 4096
	directBufferLen = 8 << 20
)

// SetWorkers sets how many regular files are copied at once.
func (c *Copier) SetWorkers(n int) {
	if n > 0 {
		c.workers = n
	}
}

// SetFaithful turns on faithful mode (see above) and direct reads.
func (c *Copier) SetFaithful(on bool) { c.faithful = on }

// keepOwnerAndMode gives dst the owner and full mode of the entry info
// describes. Owners need root; without it they are left as created.
func keepOwnerAndMode(dst string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if ok && os.Geteuid() == 0 {
		if err := os.Lchown(dst, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !ok {
		return nil
	}
	// After the chown, which clears setuid and setgid.
	return syscall.Chmod(dst, st.Mode&0o7777)
}

// makeSpecial makes the device node or FIFO info describes at dst.
func makeSpecial(dst string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	_ = os.Remove(dst)
	return syscall.Mknod(dst, st.Mode, int(st.Rdev))
}

// alignedBuffer returns a buffer O_DIRECT accepts: its address aligned to
// the block size.
func alignedBuffer(size int) []byte {
	b := make([]byte, size+directIOAlign)
	off := 0
	if r := int(uintptr(unsafe.Pointer(&b[0])) % directIOAlign); r != 0 {
		off = directIOAlign - r
	}
	return b[off : off+size : off+size]
}

// copyDirect copies srcPath into out, reading with O_DIRECT where the
// filesystem allows and through the page cache where it does not.
func copyDirect(out io.Writer, srcPath string, buf []byte) (int64, error) {
	in, err := os.OpenFile(srcPath, os.O_RDONLY|syscall.O_DIRECT, 0)
	if err != nil {
		if !errors.Is(err, syscall.EINVAL) {
			return 0, err
		}
		if in, err = os.Open(srcPath); err != nil { // no O_DIRECT here
			return 0, err
		}
	}
	defer func() { _ = in.Close() }()
	var n int64
	for {
		r, rerr := in.Read(buf)
		if errors.Is(rerr, syscall.EINVAL) && r == 0 {
			// A short read left the offset unaligned for O_DIRECT: go on
			// through the page cache from where it stopped.
			_ = in.Close()
			if in, rerr = os.Open(srcPath); rerr != nil {
				return n, rerr
			}
			if _, rerr = in.Seek(n, io.SeekStart); rerr != nil {
				return n, rerr
			}
			continue
		}
		if r > 0 {
			w, werr := out.Write(buf[:r])
			n += int64(w)
			if werr != nil {
				return n, werr
			}
		}
		if rerr == io.EOF {
			return n, nil
		}
		if rerr != nil {
			return n, rerr
		}
	}
}
