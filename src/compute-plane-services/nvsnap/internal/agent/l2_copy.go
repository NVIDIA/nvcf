// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// The copy of a checkpoint from its L2 volume onto the node: a TB and more
// per rank of a large model, in chunk files of up to 64 MiB. Reads bypass
// the page cache (O_DIRECT): buffered reads of the volume top out near
// 2 GB/s per node, direct ones run several times that with enough of them
// in flight. Writes go through the page cache: the restore reads the
// chunks straight after, and finds them there.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	l2CopyDefaultWorkers = 32
	l2CopyWorkersEnv     = "NVSNAP_L2_FETCH_WORKERS"
	l2CopyBufSize        = 8 << 20
	directIOAlign        = 4096
)

func l2CopyWorkers() int {
	if n, err := strconv.Atoi(os.Getenv(l2CopyWorkersEnv)); err == nil && n > 0 {
		return n
	}
	return l2CopyDefaultWorkers
}

type l2CopyFile struct {
	rel  string
	size int64
	mode fs.FileMode
}

// copyTreeDirect copies the tree at src into dst in place: directories
// that exist are kept, files are rewritten. Paths in skip (relative,
// leading "/") are left out. It returns the bytes and files copied.
func copyTreeDirect(ctx context.Context, src, dst string, skip []string, workers int) (int64, int, error) {
	var files []l2CopyFile
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		for _, s := range skip {
			if "/"+rel == s {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if err := os.MkdirAll(filepath.Join(dst, rel), info.Mode().Perm()|0o700); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			files = append(files, l2CopyFile{rel: rel, size: info.Size(), mode: info.Mode().Perm()})
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("walk %s: %w", src, err)
	}
	// Largest first, so no big file starts last and runs alone.
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan l2CopyFile)
	var total atomic.Int64
	var once sync.Once
	var firstErr error
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := alignedBuffer(l2CopyBufSize)
			for f := range work {
				n, err := copyFileDirect(ctx, filepath.Join(src, f.rel), filepath.Join(dst, f.rel), f.mode, buf)
				total.Add(n)
				if err != nil {
					once.Do(func() { firstErr = fmt.Errorf("%s: %w", f.rel, err); cancel() })
				}
			}
		}()
	}
feed:
	for _, f := range files {
		select {
		case work <- f:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return total.Load(), len(files), firstErr
	}
	return total.Load(), len(files), ctx.Err()
}

// copyFileDirect copies one file, reading it with O_DIRECT where the
// filesystem allows.
func copyFileDirect(ctx context.Context, srcPath, dstPath string, mode fs.FileMode, buf []byte) (int64, error) {
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
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	var n int64
	for {
		if ctx.Err() != nil {
			_ = out.Close()
			return n, ctx.Err()
		}
		r, rerr := in.Read(buf)
		if errors.Is(rerr, syscall.EINVAL) && r == 0 {
			// A short read left the offset unaligned for O_DIRECT: go on
			// through the page cache from where it stopped.
			_ = in.Close()
			if in, rerr = os.Open(srcPath); rerr != nil {
				_ = out.Close()
				return n, rerr
			}
			if _, rerr = in.Seek(n, io.SeekStart); rerr != nil {
				_ = out.Close()
				return n, rerr
			}
			continue
		}
		if r > 0 {
			w, werr := out.Write(buf[:r])
			n += int64(w)
			if werr != nil {
				_ = out.Close()
				return n, werr
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			return n, rerr
		}
	}
	return n, out.Close()
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
