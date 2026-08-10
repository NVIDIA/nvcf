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

// splice-parallel.go — N-parallel-stream splice download of a single
// file via HTTP Range requests. Each goroutine: dial TCP, send GET +
// Range header, parse headers, io.CopyN(file, conn, len) at the chunk
// offset. The CopyN against *net.TCPConn triggers Linux splice in Go
// stdlib (socket → pipe → file, zero userspace memcpy).
//
// This isolates the receive-path question: with N parallel kernel
// splices to one file at disjoint offsets, can we hit the disk-write
// ceiling (~2.9 GB/s on md0) or are we still capped by something else?
//
// Usage:
//   splice-parallel <url> <total_size_bytes> <dst> <N_workers>

// Package main implements an N-parallel-stream splice download of a
// single file via HTTP Range requests.
package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func dupFD(fd int) (int, error) { return syscall.Dup(fd) }

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: splice-parallel <url> <size> <dst> <workers>")
		os.Exit(2)
	}
	srcURL := os.Args[1]
	totalSize, _ := strconv.ParseInt(os.Args[2], 10, 64)
	dst := os.Args[3]
	workers, _ := strconv.Atoi(os.Args[4])

	u, err := url.Parse(srcURL)
	check(err)
	addr := u.Host
	if !strings.Contains(addr, ":") {
		addr += ":80"
	}

	// Pre-allocate destination.
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	check(err)
	check(f.Truncate(totalSize))

	chunkSize := (totalSize + int64(workers) - 1) / int64(workers)

	t0 := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		start := int64(i) * chunkSize
		if start >= totalSize {
			break
		}
		end := start + chunkSize - 1
		if end >= totalSize {
			end = totalSize - 1
		}
		wg.Add(1)
		go func(start, end int64) {
			defer wg.Done()
			if err := fetchRange(addr, u, start, end, f); err != nil {
				errCh <- err
			}
		}(start, end)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		fmt.Fprintln(os.Stderr, "worker err:", e)
		os.Exit(1)
	}

	check(f.Sync())
	check(f.Close())

	elapsed := time.Since(t0)
	mbps := float64(totalSize) / elapsed.Seconds() / (1 << 20)
	gbps := mbps / 1024.0
	fmt.Printf("workers=%d bytes=%d elapsed=%v throughput=%.2f MB/s = %.2f GB/s\n",
		workers, totalSize, elapsed, mbps, gbps)
}

func fetchRange(addr string, u *url.URL, start, end int64, f *os.File) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nRange: bytes=%d-%d\r\nConnection: close\r\nUser-Agent: splice-parallel\r\n\r\n",
		u.RequestURI(), u.Host, start, end)
	if _, werr := conn.Write([]byte(req)); werr != nil {
		return fmt.Errorf("write request: %w", werr)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 206") && !strings.HasPrefix(statusLine, "HTTP/1.1 200") {
		return fmt.Errorf("bad status: %s", statusLine)
	}
	for {
		line, lerr := reader.ReadString('\n')
		if lerr != nil {
			return lerr
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}

	// Drain bufio's prefetched body bytes via positional WriteAt (NOT
	// splice — these bytes are already in userspace).
	offset := start
	buffered := reader.Buffered()
	if buffered > 0 {
		peek, perr := reader.Peek(buffered)
		if perr != nil {
			return perr
		}
		if _, waerr := f.WriteAt(peek, offset); waerr != nil {
			return waerr
		}
		_, _ = reader.Discard(buffered)
		offset += int64(buffered)
	}

	// remaining body bytes for this Range = end - start + 1 - buffered
	remaining := end - start + 1 - int64(buffered)
	if remaining <= 0 {
		return nil
	}

	// Splice path via *os.File.ReadFrom(conn) is not positional. Need
	// to use a per-worker temp file then move bytes into the correct
	// offset of the destination file. To keep the splice in play AND
	// avoid an intermediate copy, open the dest at the chunk offset
	// via a file descriptor cloned to point there, and call ReadFrom.
	//
	// Trick: dup the destination fd, lseek the dup to offset, treat
	// the dup as an io.Writer. *os.File implements ReadFrom which
	// uses splice for *net.TCPConn source on Linux (Go 1.21+).
	fd, err := dupFile(f, offset)
	if err != nil {
		return fmt.Errorf("dup: %w", err)
	}
	defer func() { _ = fd.Close() }()

	n, err := io.CopyN(fd, conn, remaining)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if n != remaining {
		return fmt.Errorf("short: %d vs %d", n, remaining)
	}
	return nil
}

// dupFile returns a new *os.File backed by the same kernel inode as
// `src`, positioned at `offset`. Use cases: parallel ReadFrom writers
// at disjoint offsets of one file. The returned File must be Closed
// (closes only the dup, not the original).
func dupFile(src *os.File, offset int64) (*os.File, error) {
	// dup3-equivalent via os.NewFile + syscall.Dup gives us a separate
	// fd with its own file position. Then Seek the new fd.
	newFd, err := dupFD(int(src.Fd()))
	if err != nil {
		return nil, err
	}
	dup := os.NewFile(uintptr(newFd), src.Name()+".dup")
	if _, serr := dup.Seek(offset, io.SeekStart); serr != nil {
		_ = dup.Close()
		return nil, serr
	}
	return dup, nil
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
