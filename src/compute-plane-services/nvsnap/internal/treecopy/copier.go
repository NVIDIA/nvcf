/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package treecopy is the shared file-tree copier used by rootfs-only
// capture. It enforces the file-type discipline a live container's
// filesystem demands (skip sockets / FIFOs / devices / whiteouts,
// preserve symlinks + hardlinks, drop trusted.overlay.* xattrs,
// tolerate ENOENT mid-walk) and is the single place that decision
// lives. Callers: the rootfsonly orchestrator (capture side) and the
// checkpointstore Local + GPDRox backends (write side).
//
// nvsnap#174 (2026-06-03): file copies are kernel-streamed via
// sendfile(2) — no userspace buffer round-trip. Regular files are
// dispatched to a worker pool so cross-FS copies (NVMe → Hyperdisk-ML)
// can saturate the destination's write throughput. The walk itself
// stays sequential because directories, symlinks, and hardlink
// dedup must run in order.
package treecopy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/sys/unix"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/tracing"
)

// Copier copies a directory tree with the file-type discipline a
// rootfs-capture needs: every Unix file type is considered explicitly,
// non-transportable types are skipped (sockets, FIFOs, devices, overlay
// whiteouts), hardlinks are deduplicated by inode, xattrs are filtered
// (drop trusted.overlay.*, keep user.*), and races against the live
// source pod (file removed mid-walk) are tolerated as warnings.
//
// All file-type and xattr decisions live in this one type so they can
// be reasoned about + tested in isolation. The orchestrator owns
// staging-dir lifecycle; Copier owns "what does it mean to copy".
//
// Concurrency: Copy is safe to invoke once per Copier. Internally it
// runs a worker pool for regular-file copies; counters and inodeMap
// are protected by atomics + mutex respectively. Multiple concurrent
// Copy calls on the same Copier would race on inodeMap and are not
// supported (the orchestrator instantiates one Copier per source tree).
type Copier struct {
	excludes    map[string]struct{} // absolute paths (e.g. "/etc/hostname")
	inodeMap    map[uint64]string   // dev<<32|ino → first dst path; second occurrence becomes hardlink
	inodeMu     sync.Mutex          // guards inodeMap (called once per regular file — cheap contention)
	bytesCopied atomic.Int64
	filesCopied atomic.Int64
	workers     int
	log         logrus.FieldLogger
}

// TrustedOverlayXattrPrefix is the prefix tar's spike uses with
// `--xattrs-exclude=trusted.overlay.*`. These overlayfs metadata xattrs
// only have meaning when read in the original overlay context, not in a
// destination pod's filesystem.
const TrustedOverlayXattrPrefix = "trusted.overlay."

// FallbackBufSize is used only when sendfile(2) is unavailable
// (older kernels) or returns ENOSYS / EINVAL — vanishingly rare on
// the GKE/EKS images we target. 1 MiB keeps syscall overhead low if
// we do hit the path.
const FallbackBufSize = 1 << 20

// defaultWorkers is the worker-pool size when NVSNAP_TREECOPY_WORKERS
// is unset. Picked empirically: Hyperdisk-ML at 10 GiB/s can absorb
// ~8 parallel writers each sustaining ~1.3 GiB/s. Going higher
// risks scheduler thrash without improving disk throughput.
const defaultWorkers = 8

// envWorkers names the env var operators use to tune copy
// parallelism without rebuilding. Values <1 fall back to default.
const envWorkers = "NVSNAP_TREECOPY_WORKERS"

// NewCopier returns a Copier that skips the given absolute exclude
// paths. A nil log gets a default logrus entry.
func NewCopier(excludes []string, log logrus.FieldLogger) *Copier {
	excludeSet := make(map[string]struct{}, len(excludes))
	for _, e := range excludes {
		if e == "" || e == "/" {
			continue
		}
		excludeSet[e] = struct{}{}
	}
	if log == nil {
		log = logrus.NewEntry(logrus.New()).WithField("subsys", "rootfsonly.tree")
	}
	return &Copier{
		excludes: excludeSet,
		inodeMap: make(map[uint64]string),
		workers:  resolveWorkers(),
		log:      log,
	}
}

// resolveWorkers picks the worker-pool size. Order:
//  1. NVSNAP_TREECOPY_WORKERS env (positive int wins)
//  2. defaultWorkers, capped at 2x GOMAXPROCS to avoid pathological
//     misconfigurations on small machines (e.g. user sets 64 on a
//     2-CPU dev box and OOMs the agent).
func resolveWorkers() int {
	if s := os.Getenv(envWorkers); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	n := defaultWorkers
	if maxN := 2 * runtime.GOMAXPROCS(0); maxN < n {
		n = maxN
	}
	if n < 1 {
		n = 1
	}
	return n
}

// fileJob is a unit of work for the copy pool. Only regular files
// are dispatched here — dirs/symlinks/devices are handled inline
// during the walk where ordering matters.
type fileJob struct {
	srcPath string
	dstPath string
	info    os.FileInfo
}

// Copy walks src and replicates entries into dst. Returns total
// regular-file bytes + entry count copied (including dirs and symlinks
// in entry count, regular bytes only in byte count).
//
// Fatal errors (cannot create dst, ctx cancelled, fundamentally broken
// fs) abort the walk. Per-entry errors (ENOENT mid-walk, permission
// denied on a single file, unsupported file type) log a warning and
// continue — the source pod is alive and writing, which is normal.
func (c *Copier) Copy(ctx context.Context, src, dst string) (bytesCopied, entriesCopied int64, retErr error) {
	ctx, span := tracing.Tracer().Start(ctx, "treecopy.copy")
	span.SetAttributes(attribute.Int("nvsnap.workers", c.workers))
	defer func() {
		span.SetAttributes(
			attribute.Int64("nvsnap.bytes_copied", c.bytesCopied.Load()),
			attribute.Int64("nvsnap.files_copied", c.filesCopied.Load()),
		)
		span.End()
	}()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, 0, fmt.Errorf("mkdir dst: %w", err)
	}
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return 0, 0, fmt.Errorf("lstat src: %w", err)
	}
	if !srcInfo.IsDir() {
		return 0, 0, fmt.Errorf("src is not a directory: %s", src)
	}
	// Apply src's mode to the dst root (best effort; ignore if dst
	// already exists with different perms).
	_ = os.Chmod(dst, srcInfo.Mode().Perm())

	// Worker pool. Cancellable via a sub-context: any worker error
	// cancels the rest so we don't keep streaming GB of files after
	// the first failure.
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan fileJob, c.workers*2)
	var firstErr atomic.Value // error
	var wg sync.WaitGroup

	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				select {
				case <-poolCtx.Done():
					// Drain — let the producer learn poolCtx is
					// done via the cancel below. We can't just
					// return because the producer would block on
					// the unbuffered tail.
					continue
				default:
				}
				if err := c.copyRegular(job.srcPath, job.dstPath, job.info); err != nil {
					// Record first fatal error and trigger pool
					// cancellation. Transient (mid-walk ENOENT)
					// errors don't get here — copyRegular swallows
					// them via IsTransientErr.
					if firstErr.CompareAndSwap(nil, err) {
						cancel()
					}
				}
			}
		}()
	}

	// Producer: filepath.Walk creates dirs/symlinks inline and
	// dispatches regular files to the worker pool. Hardlink dedup
	// runs in the producer too (mutex-protected inodeMap) — every
	// file passes through the producer once before potentially
	// being dispatched.
	walkErr := filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		// Walk-time error: typically ENOENT for an entry that vanished
		// mid-walk, or EACCES for a directory we can't list. Log and
		// continue — the live pod is allowed to mutate its own fs.
		if walkErr != nil {
			if IsTransientErr(walkErr) {
				c.log.WithError(walkErr).WithField("path", path).Debug("walk: transient error, skipping")
				if info != nil && info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return walkErr
		}
		select {
		case <-poolCtx.Done():
			return poolCtx.Err()
		default:
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if c.isExcluded(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dstPath := filepath.Join(dst, rel)
		return c.dispatch(poolCtx, jobs, path, dstPath, info)
	})

	// Producer done — no more jobs. Close channel so workers can
	// exit, then wait for them.
	close(jobs)
	wg.Wait()

	if walkErr != nil {
		// Walk error supersedes a worker error: walk failure usually
		// indicates the source tree is gone or the agent's cgroup is
		// being killed. Either way, walk-error is more diagnostic.
		return c.bytesCopied.Load(), c.filesCopied.Load(), walkErr
	}
	if e := firstErr.Load(); e != nil {
		return c.bytesCopied.Load(), c.filesCopied.Load(), e.(error)
	}
	return c.bytesCopied.Load(), c.filesCopied.Load(), nil
}

// dispatch handles one walk entry. Directories, symlinks, devices,
// FIFOs/sockets, and hardlink dedup happen inline (cheap, must
// preserve order). Regular files (non-hardlink) go to the worker pool.
func (c *Copier) dispatch(ctx context.Context, jobs chan<- fileJob, srcPath, dstPath string, info os.FileInfo) error {
	mode := info.Mode()
	switch {
	case mode.IsDir():
		c.filesCopied.Add(1)
		if err := os.MkdirAll(dstPath, mode.Perm()); err != nil {
			return err
		}
		return c.copyXattrs(srcPath, dstPath)
	case mode&os.ModeSymlink != 0:
		return c.copySymlink(srcPath, dstPath, info)
	case mode.IsRegular():
		// Hardlink dedup. Two cases:
		//   1. Second+ occurrence (inodeMap hit): emit os.Link inline.
		//      The first occurrence was copied inline (see case 2)
		//      so the source path is guaranteed present on disk —
		//      no race with the worker pool.
		//   2. First occurrence of an nlink>=2 file: copy INLINE in
		//      the producer. Subsequent hardlinks need this file to
		//      exist before os.Link can succeed; dispatching to the
		//      worker pool would race. nlink>=2 files are rare in
		//      CRIU dumps, so the parallelism loss is negligible.
		//   3. nlink==1 (the common case): dispatch to the pool.
		hardlinkSrc, isLink, isFirstLink := c.classifyHardlink(dstPath, info)
		if isLink {
			if err := os.Link(hardlinkSrc, dstPath); err != nil {
				// Fall through to copy if Link fails (cross-device, ENOSYS).
				c.log.WithError(err).WithField("path", dstPath).Debug("hardlink failed; falling back to copy")
			} else {
				c.filesCopied.Add(1)
				return nil
			}
		}
		if isFirstLink {
			// Inline copy guarantees the file exists before any
			// downstream hardlink dispatch attempts os.Link.
			return c.copyRegular(srcPath, dstPath, info)
		}
		select {
		case jobs <- fileJob{srcPath: srcPath, dstPath: dstPath, info: info}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case mode&os.ModeDevice != 0:
		// Char/block device. Whiteouts (overlayfs deletion markers) are
		// char-dev 0:0; everything else (real /dev/* mounts) is excluded
		// by the mountinfo-driven excludes already. Skip silently.
		c.log.WithField("path", srcPath).Debug("skip device file")
		return nil
	case mode&os.ModeNamedPipe != 0, mode&os.ModeSocket != 0:
		// FIFOs and Unix sockets are by-process state — not transportable.
		c.log.WithField("path", srcPath).WithField("type", mode.Type().String()).Debug("skip non-transportable file type")
		return nil
	default:
		// Unknown bit set — log and skip. Keeps Copy permissive in face
		// of weird Linux types we haven't seen yet.
		c.log.WithField("path", srcPath).WithField("mode", mode.String()).Warn("skip unknown file type")
		return nil
	}
}

// isExcluded returns true if rel (a relative path within src, e.g.
// "etc/hostname") matches an exact exclude or is a descendant of an
// excluded directory.
func (c *Copier) isExcluded(rel string) bool {
	if len(c.excludes) == 0 {
		return false
	}
	abs := "/" + filepath.ToSlash(rel)
	if _, ok := c.excludes[abs]; ok {
		return true
	}
	for p := abs; p != "/" && p != "."; p = filepath.Dir(p) {
		if _, ok := c.excludes[p]; ok {
			return true
		}
	}
	return false
}

// copyRegular streams srcPath to dstPath via sendfile(2). Kernel-side
// transfer — no userspace buffer round-trip. Works cross-filesystem
// (NVMe → Hyperdisk-ML in our deployment) since sendfile supports any
// regular-file in/out pair on Linux 2.6.33+.
//
// Fallback: if sendfile fails with EINVAL or ENOSYS (kernel too old,
// or one of the FDs is a special file type that sendfile can't
// handle), we drop to io.CopyBuffer with a 1 MiB pooled buffer.
func (c *Copier) copyRegular(srcPath, dstPath string, info os.FileInfo) error {
	in, err := os.Open(srcPath)
	if err != nil {
		if IsTransientErr(err) {
			c.log.WithError(err).WithField("path", srcPath).Debug("source removed mid-walk; skipping")
			return nil
		}
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create dst: %w", err)
	}

	written, err := streamCopy(out, in, info.Size())
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	c.bytesCopied.Add(written)
	c.filesCopied.Add(1)
	if err := c.copyXattrs(srcPath, dstPath); err != nil {
		return err
	}
	return preserveTimes(dstPath, info, false)
}

// preserveTimes stamps dst's atime+mtime to match src at nanosecond
// precision. This is mandatory, not cosmetic: ninja-based JIT caches
// (sgl_kernel / tvm-ffi, used by SGLang) decide reuse-vs-rebuild by
// comparing each file's on-disk mtime against the build times recorded
// inside .ninja_log/.ninja_deps. A plain copy stamps every output with
// the copy time (minutes after the real build), so ninja sees the
// outputs as "modified after build" and the inputs as "newer than last
// build" and recompiles the entire cache — even though it is complete
// and at the matching path (gemma-4-31B SGLang, all 8 kernels rebuilt
// every restore, 2026-06-19). Content-keyed caches (triton, inductor,
// deep_gemm) are copy-safe; the ninja ones are not.
//
// symlink=true sets AT_SYMLINK_NOFOLLOW so the link itself is stamped,
// not its target. Best-effort: a non-Stat_t FileInfo (impossible on
// Linux) is a no-op rather than an error.
func preserveTimes(dstPath string, info os.FileInfo, symlink bool) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	times := []unix.Timespec{
		unix.NsecToTimespec(st.Atim.Nano()),
		unix.NsecToTimespec(st.Mtim.Nano()),
	}
	flags := 0
	if symlink {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	return unix.UtimesNanoAt(unix.AT_FDCWD, dstPath, times, flags)
}

// streamCopy moves bytes from in → out via sendfile(2). Loops on
// short returns (sendfile can transfer less than requested). Returns
// total bytes written.
//
// Falls back to io.CopyBuffer on EINVAL or ENOSYS — the two errno
// values that indicate sendfile is structurally unavailable for this
// FD pair (vs transient I/O errors which surface as EIO etc and stay
// in the sendfile path so the caller sees them).
func streamCopy(out, in *os.File, size int64) (int64, error) {
	// Empty files: sendfile returns 0 with no error, which our loop
	// would treat as done. Short-circuit to avoid the syscall.
	if size == 0 {
		return 0, nil
	}

	var total int64
	for total < size {
		// nil offset means "use the input file's current position";
		// sendfile advances it after each call, so consecutive calls
		// pick up where the last left off.
		n, err := unix.Sendfile(int(out.Fd()), int(in.Fd()), nil, int(size-total))
		if err != nil {
			if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOSYS) {
				// Sendfile structurally can't do this pair. Reset
				// the input offset to where total stops (sendfile
				// advanced it on partial success) and fall back.
				if _, sErr := in.Seek(total, io.SeekStart); sErr != nil {
					return total, fmt.Errorf("sendfile fallback: seek in: %w", sErr)
				}
				if _, sErr := out.Seek(total, io.SeekStart); sErr != nil {
					return total, fmt.Errorf("sendfile fallback: seek out: %w", sErr)
				}
				buf := make([]byte, FallbackBufSize)
				n64, copyErr := io.CopyBuffer(out, in, buf)
				return total + n64, copyErr
			}
			return total, fmt.Errorf("sendfile: %w", err)
		}
		if n == 0 {
			// Source EOF before size reached. Common for live-pod
			// captures where a file shrank mid-walk. Return what
			// we got; caller treats this as a successful (partial)
			// copy.
			break
		}
		total += int64(n)
	}
	return total, nil
}

// classifyHardlink inspects info's link count + inodeMap state and
// returns one of three states (encoded as the booleans):
//
//	hardlinkSrc, true,  false  → this is a SUBSEQUENT occurrence of
//	                              an already-seen inode; caller does
//	                              os.Link(hardlinkSrc, dstPath).
//	"",          false, true   → this is the FIRST occurrence of an
//	                              nlink>=2 inode; caller must copy
//	                              the file INLINE (not dispatch) so
//	                              later occurrences find it.
//	"",          false, false  → ordinary nlink<2 file; caller may
//	                              dispatch to the worker pool.
//
// Mutex-protected because the producer calls this — it serializes
// hardlink decisions while the worker pool streams unrelated files.
// (Mutex is held only for the map check; no I/O happens under it.)
func (c *Copier) classifyHardlink(dstPath string, info os.FileInfo) (hardlinkSrc string, isLink, isFirstLink bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink < 2 {
		return "", false, false
	}
	key := stat.Dev<<32 | stat.Ino
	c.inodeMu.Lock()
	defer c.inodeMu.Unlock()
	if existing, ok := c.inodeMap[key]; ok {
		return existing, true, false
	}
	c.inodeMap[key] = dstPath
	return "", false, true
}

// copySymlink replicates the link without following it. The target
// stays a literal string; if it points outside src, the destination
// link will be broken — that's correct for capture semantics (we
// preserve what was there, not what it resolved to in the source pod).
func (c *Copier) copySymlink(srcPath, dstPath string, info os.FileInfo) error {
	target, err := os.Readlink(srcPath)
	if err != nil {
		if IsTransientErr(err) {
			return nil
		}
		return err
	}
	_ = os.Remove(dstPath) // tolerate prior file at dst
	if err := os.Symlink(target, dstPath); err != nil {
		return err
	}
	c.filesCopied.Add(1)
	return preserveTimes(dstPath, info, true)
}

// copyXattrs copies extended attributes from srcPath to dstPath, skipping
// trusted.overlay.* (overlayfs metadata that doesn't transfer).
//
// On filesystems / kernels that don't support listxattr, returns nil
// silently — xattrs are best-effort.
func (c *Copier) copyXattrs(srcPath, dstPath string) error {
	names, err := xattrList(srcPath)
	if err != nil {
		// No xattrs supported / permission denied / file gone — skip.
		return nil
	}
	for _, name := range names {
		if strings.HasPrefix(name, TrustedOverlayXattrPrefix) {
			continue
		}
		val, err := xattrGet(srcPath, name)
		if err != nil {
			continue
		}
		_ = xattrSet(dstPath, name, val)
	}
	return nil
}

// IsTransientErr returns true for errors that mean "the source pod
// modified its filesystem during our walk". Common cases:
//   - ENOENT: file removed
//   - ESTALE: NFS file handle stale
//   - EACCES: file permissions changed (rare but possible)
func IsTransientErr(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOENT, syscall.ESTALE, syscall.EACCES:
			return true
		}
	}
	return false
}

// xattrList wraps unix.Listxattr with the standard "grow until fits" idiom.
func xattrList(path string) ([]string, error) {
	size, err := unix.Listxattr(path, nil)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := unix.Listxattr(path, buf)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for name := range strings.SplitSeq(string(buf[:n]), "\x00") {
		if name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

func xattrGet(path, name string) ([]byte, error) {
	size, err := unix.Getxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := unix.Getxattr(path, name, buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

func xattrSet(path, name string, val []byte) error {
	return unix.Setxattr(path, name, val, 0)
}
