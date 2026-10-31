// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Extraction of a capture's archives (the rootfs diff, the replay-mount
// snapshots) into a placeholder, by the agent, through a directory opened
// under the placeholder's root (confined_fs.go): no tool runs in the
// placeholder, and no path resolves outside its root. It does what the
// GNU tar extraction it replaces did (--xattrs --acls --numeric-owner
// --preserve-permissions --overwrite): owners by number, modes with their
// special bits, modification times, extended attributes, POSIX ACLs, and
// holes in sparse files.

import (
	"archive/tar"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// tarExtractOpts says what an extraction leaves out.
type tarExtractOpts struct {
	// skip reports an entry (a clean relative path) not to extract, nor
	// anything under it when it is a directory.
	skip func(name string) bool
	// skipXattr reports an extended attribute not to restore.
	skipXattr func(name string) bool
}

// extractTar extracts the archive r into d. It returns the entries it
// extracted.
func extractTar(r io.Reader, d *confinedDir, o tarExtractOpts) (int, error) {
	const dir = "."
	tr := tar.NewReader(r)
	type dirTimes struct {
		name  string
		mtime time.Time
	}
	var dirs []dirTimes // set after their contents, which change them
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return n, fmt.Errorf("read the archive: %w", err)
		}
		rel, ok := cleanTarName(h.Name)
		if !ok {
			return n, fmt.Errorf("archive entry %q leaves the extraction directory", h.Name)
		}
		if rel == "." && h.Typeflag != tar.TypeDir {
			continue
		}
		if o.skip != nil && rel != "." && o.skip(rel) {
			continue
		}
		name := path.Join(dir, rel)
		if err := extractEntry(tr, h, d, dir, name); err != nil {
			return n, fmt.Errorf("extract %s: %w", rel, err)
		}
		if err := restoreMeta(h, d, name, o); err != nil {
			return n, fmt.Errorf("extract %s: %w", rel, err)
		}
		if h.Typeflag == tar.TypeDir {
			dirs = append(dirs, dirTimes{name, h.ModTime})
		}
		n++
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := setTimes(d, dirs[i].name, dirs[i].mtime); err != nil {
			return n, fmt.Errorf("extract %s: %w", dirs[i].name, err)
		}
	}
	return n, nil
}

// cleanTarName is an entry's name relative to the extraction directory,
// and false for one that would leave it.
func cleanTarName(name string) (string, bool) {
	c := path.Clean("/" + strings.TrimPrefix(name, "./"))
	if strings.Contains(name, "..") {
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return "", false
			}
		}
	}
	if c == "/" {
		return ".", true
	}
	return strings.TrimPrefix(c, "/"), true
}

// extractEntry creates entry h at name, replacing what is there, except a
// directory, which is kept (its metadata is restored). A hard link's target
// is named relative to the extraction directory dir.
func extractEntry(tr *tar.Reader, h *tar.Header, d *confinedDir, dir, name string) error {
	if err := d.root.MkdirAll(path.Dir(name), 0o755); err != nil {
		return err
	}
	if h.Typeflag == tar.TypeDir {
		if fi, err := d.root.Lstat(name); err == nil {
			if fi.IsDir() {
				return nil
			}
			if err := d.root.Remove(name); err != nil {
				return err
			}
		}
		return d.root.Mkdir(name, 0o700) // its mode is set with its owner
	}
	if fi, err := d.root.Lstat(name); err == nil {
		if fi.IsDir() {
			err = d.root.RemoveAll(name)
		} else {
			err = d.root.Remove(name)
		}
		if err != nil {
			return err
		}
	}
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeGNUSparse:
		f, err := d.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			return err
		}
		if err := copySparse(f, tr, h.Size); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	case tar.TypeSymlink:
		return d.root.Symlink(h.Linkname, name)
	case tar.TypeLink:
		target, ok := cleanTarName(h.Linkname)
		if !ok {
			return fmt.Errorf("hard link to %q leaves the extraction directory", h.Linkname)
		}
		return d.root.Link(path.Join(dir, target), name)
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		mode := uint32(unix.S_IFIFO)
		switch h.Typeflag {
		case tar.TypeChar:
			mode = unix.S_IFCHR
		case tar.TypeBlock:
			mode = unix.S_IFBLK
		}
		return mknodAt(d, name, mode|0o600, unix.Mkdev(uint32(h.Devmajor), uint32(h.Devminor))) //nolint:gosec // device numbers from the archive's header fields
	default:
		return fmt.Errorf("unsupported entry type %q", h.Typeflag)
	}
}

// copySparse writes size bytes of r to f, leaving runs of zeros as holes,
// as tar --sparse restores them.
func copySparse(f *os.File, r io.Reader, size int64) error {
	const block = 1 << 20
	buf := make([]byte, block)
	var off int64
	for off < size {
		n, err := io.ReadFull(r, buf[:min(int64(block), size-off)])
		if n > 0 {
			if allZero(buf[:n]) {
				if _, serr := f.Seek(int64(n), io.SeekCurrent); serr != nil {
					return serr
				}
			} else if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			off += int64(n)
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			if errors.Is(err, io.EOF) && off == size {
				break
			}
			return err
		}
	}
	return f.Truncate(size)
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// restoreMeta restores entry h's owner, mode, extended attributes, ACLs
// and (but for a directory, set at the end) modification time at name.
func restoreMeta(h *tar.Header, d *confinedDir, name string, o tarExtractOpts) error {
	if h.Typeflag == tar.TypeLink {
		return nil // the target's
	}
	parent, err := d.root.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	pfd, base := int(parent.Fd()), path.Base(name)
	if err := unix.Fchownat(pfd, base, h.Uid, h.Gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("owner: %w", err)
	}
	if h.Typeflag == tar.TypeSymlink {
		return setTimes(d, name, h.ModTime)
	}
	// After the owner: chown clears the set-id bits.
	if err := unix.Fchmodat(pfd, base, uint32(h.Mode)&0o7777, 0); err != nil { //nolint:gosec // permission bits of the archive's mode
		return fmt.Errorf("mode: %w", err)
	}
	if err := restoreXattrs(h, pfd, base, o); err != nil {
		return err
	}
	if h.Typeflag == tar.TypeDir {
		return nil
	}
	return setTimes(d, name, h.ModTime)
}

// restoreXattrs sets the entry's extended attributes and POSIX ACLs (GNU
// tar's SCHILY.xattr.* and SCHILY.acl.* records) on base in directory pfd.
func restoreXattrs(h *tar.Header, pfd int, base string, o tarExtractOpts) error {
	set := func(attr string, value []byte) error {
		fd, err := unix.Openat(pfd, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		return unix.Fsetxattr(fd, attr, value, 0)
	}
	for k, v := range h.PAXRecords {
		switch {
		case strings.HasPrefix(k, "SCHILY.xattr."):
			attr := strings.TrimPrefix(k, "SCHILY.xattr.")
			if o.skipXattr != nil && o.skipXattr(attr) {
				continue
			}
			if err := set(attr, []byte(v)); err != nil {
				return fmt.Errorf("xattr %s: %w", attr, err)
			}
		case k == "SCHILY.acl.access" || k == "SCHILY.acl.default":
			acl, err := encodePosixACL(v)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			attr := "system.posix_acl_access"
			if k == "SCHILY.acl.default" {
				attr = "system.posix_acl_default"
			}
			if err := set(attr, acl); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	return nil
}

// encodePosixACL encodes a POSIX ACL in its text form ("user::rwx,
// user:1000:r--,group::r-x,mask::r-x,other::r--", numeric ids, entries
// separated by commas or newlines) as the kernel's xattr value.
func encodePosixACL(text string) ([]byte, error) {
	const (
		version   = 2
		tagUserOb = 0x01
		tagUser   = 0x02
		tagGroupO = 0x04
		tagGroup  = 0x08
		tagMask   = 0x10
		tagOther  = 0x20
		undefID   = 0xffffffff
	)
	out := binary.LittleEndian.AppendUint32(nil, version)
	for _, e := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' }) {
		f := strings.Split(strings.TrimSpace(e), ":")
		if len(f) < 3 {
			return nil, fmt.Errorf("ACL entry %q", e)
		}
		var tag uint16
		id := uint32(undefID)
		switch f[0] {
		case "user", "u":
			tag = tagUserOb
			if f[1] != "" {
				tag = tagUser
			}
		case "group", "g":
			tag = tagGroupO
			if f[1] != "" {
				tag = tagGroup
			}
		case "mask", "m":
			tag = tagMask
		case "other", "o":
			tag = tagOther
		default:
			return nil, fmt.Errorf("ACL entry %q", e)
		}
		if tag == tagUser || tag == tagGroup {
			n, err := strconv.ParseUint(f[1], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("ACL entry %q: id: %w", e, err)
			}
			id = uint32(n)
		}
		var perm uint16
		for _, c := range f[2] {
			switch c {
			case 'r':
				perm |= 4
			case 'w':
				perm |= 2
			case 'x':
				perm |= 1
			case '-':
			default:
				return nil, fmt.Errorf("ACL entry %q: permissions", e)
			}
		}
		out = binary.LittleEndian.AppendUint16(out, tag)
		out = binary.LittleEndian.AppendUint16(out, perm)
		out = binary.LittleEndian.AppendUint32(out, id)
	}
	return out, nil
}

// setTimes sets name's access and modification times to mtime, without
// following a symlink.
func setTimes(d *confinedDir, name string, mtime time.Time) error {
	parent, err := d.root.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	ts := unix.NsecToTimespec(mtime.UnixNano())
	return unix.UtimesNanoAt(int(parent.Fd()), path.Base(name), []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
}

// mknodAt creates a device node or FIFO at name, through its parent opened
// under d's root.
func mknodAt(d *confinedDir, name string, mode uint32, dev uint64) error {
	parent, err := d.root.Open(path.Dir(name))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if dev > uint64(^uint(0)>>1) {
		return fmt.Errorf("device number %d out of range", dev)
	}
	return unix.Mknodat(int(parent.Fd()), path.Base(name), mode, int(dev))
}
