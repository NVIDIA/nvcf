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

// Package main implements splice-microbench — measure raw TCP +
// Go-stdlib splice throughput.
//
// Connects to a peer agent's /v1/checkpoints/{id}/file endpoint over raw
// TCP. Sends a minimal HTTP/1.1 GET. Reads the response status + headers
// via bufio.Reader. Drains any pre-buffered body bytes, then calls
// `io.CopyN(file, conn, remaining)` — on Linux this triggers splice
// (socket → pipe → file) when conn is *net.TCPConn. Zero userspace
// memcpy for the body.
//
// Why this is the right microbench: it isolates the receive path. Sender
// is the unchanged nvsnap-agent (uses http.ServeContent → sendfile, kernel
// zero-copy on send). If receiver also avoids userspace memcpy via
// splice, we should see throughput approach disk-write ceiling
// (measured ~2.9 GB/s parallel write to md0).
//
// Usage:
//
//	go run splice-microbench.go <url> <dst-file>
//
// Example:
//
//	go run splice-microbench.go \
//	  "http://10.0.0.51:8081/v1/checkpoints/<id>/file?path=pages-14.img" \
//	  /var/lib/nvsnap/checkpoints/splice-test.bin
//
// Build for cluster: GOOS=linux go build -o /tmp/splice-microbench scripts/splice-microbench.go
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
	"time"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: splice-microbench <url> <dst>")
		os.Exit(2)
	}
	srcURL, dst := os.Args[1], os.Args[2]
	u, err := url.Parse(srcURL)
	check(err)

	addr := u.Host
	if !strings.Contains(addr, ":") {
		addr += ":80"
	}

	t0 := time.Now()

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	check(err)
	defer func() { _ = conn.Close() }()

	// Send a minimal HTTP/1.1 GET. Connection: close so the server
	// signals end-of-body by closing the socket — simplifies framing.
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: splice-microbench\r\n\r\n",
		u.RequestURI(), u.Host)
	_, err = conn.Write([]byte(req))
	check(err)

	reader := bufio.NewReader(conn)

	statusLine, err := reader.ReadString('\n')
	check(err)
	if !strings.HasPrefix(statusLine, "HTTP/1.1 200") && !strings.HasPrefix(statusLine, "HTTP/1.1 206") {
		fmt.Fprintf(os.Stderr, "bad status: %s", statusLine)
		os.Exit(1) //nolint:gocritic // process exit; OS reclaims the socket fd, deferred Close is moot
	}

	var contentLength int64 = -1
	for {
		line, err2 := reader.ReadString('\n')
		check(err2)
		trim := strings.TrimRight(line, "\r\n")
		if trim == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(trim), "content-length:") {
			v := strings.TrimSpace(trim[len("content-length:"):])
			contentLength, err2 = strconv.ParseInt(v, 10, 64)
			check(err2)
		}
	}
	if contentLength < 0 {
		fmt.Fprintln(os.Stderr, "no content-length header")
		os.Exit(1)
	}

	headerTime := time.Since(t0)

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	check(err)
	defer func() { _ = f.Close() }()

	// Drain bufio's buffered body bytes (read past headers) into file
	// directly. These bytes are already in userspace; no splice for
	// them, but it's usually a few KB at most.
	buffered := reader.Buffered()
	var bufferedDrained int
	if buffered > 0 {
		peek, err2 := reader.Peek(buffered)
		check(err2)
		n, err2 := f.Write(peek)
		check(err2)
		bufferedDrained = n
		_, err2 = reader.Discard(buffered)
		check(err2)
	}

	remaining := contentLength - int64(bufferedDrained)
	if remaining < 0 {
		remaining = 0
	}

	// THE KEY CALL: io.CopyN with *os.File destination and *net.TCPConn
	// source triggers Linux splice in Go stdlib (via internal/poll.FD.ReadFrom
	// which uses splice through a pipe for socket → file). Zero userspace
	// memcpy for these bytes.
	written, err := io.CopyN(f, conn, remaining)
	check(err)
	if written != remaining {
		fmt.Fprintf(os.Stderr, "short read: %d vs %d", written, remaining)
		os.Exit(1)
	}

	check(f.Sync())

	totalTime := time.Since(t0)
	bodyTime := totalTime - headerTime

	mbps := float64(contentLength) / bodyTime.Seconds() / (1 << 20)
	gbps := mbps / 1024.0
	fmt.Printf("Headers: %v\n", headerTime)
	fmt.Printf("Body:    %v\n", bodyTime)
	fmt.Printf("Total:   %v\n", totalTime)
	fmt.Printf("Bytes:   %d (Content-Length)\n", contentLength)
	fmt.Printf("Body throughput: %.2f MB/s = %.2f GB/s\n", mbps, gbps)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
