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

package streamer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pierrec/lz4/v4"
	"golang.org/x/sys/unix"
)

// TestControlSocketReadRequest verifies protobuf wire encoding/decoding.
func TestControlSocketReadRequest(t *testing.T) {
	tests := []string{"pages-1.img", "core-1.img", "fdinfo-2.img", "a"}

	for _, filename := range tests {
		t.Run(filename, func(t *testing.T) {
			srv, cli := socketPair(t)
			defer func() { _ = srv.Close() }()
			defer func() { _ = cli.Close() }()

			cs, err := newControlSocket(srv)
			if err != nil {
				t.Fatal(err)
			}

			go func() {
				sendProtoRequest(cli, filename)
				_ = cli.Close()
			}()

			got, err := cs.readRequest()
			if err != nil {
				t.Fatalf("readRequest: %v", err)
			}
			if got != filename {
				t.Fatalf("got %q, want %q", got, filename)
			}
		})
	}
}

// TestControlSocketEOF verifies clean EOF handling.
func TestControlSocketEOF(t *testing.T) {
	srv, cli := socketPair(t)
	_ = cli.Close()

	cs, _ := newControlSocket(srv)
	_, err := cs.readRequest()
	_ = srv.Close()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

// TestControlSocketWriteReply verifies reply wire format.
func TestControlSocketWriteReply(t *testing.T) {
	for _, exists := range []bool{true, false} {
		t.Run(boolStr(exists), func(t *testing.T) {
			srv, cli := socketPair(t)
			defer func() { _ = srv.Close() }()
			defer func() { _ = cli.Close() }()

			cs, _ := newControlSocket(srv)
			go func() {
				_ = cs.writeReply(exists)
				_ = srv.Close()
			}()

			var hdr [4]byte
			_, _ = io.ReadFull(cli, hdr[:])
			size := binary.LittleEndian.Uint32(hdr[:])
			if size != 2 {
				t.Fatalf("expected size 2, got %d", size)
			}
			payload := make([]byte, size)
			_, _ = io.ReadFull(cli, payload)
			if payload[0] != 0x08 {
				t.Fatalf("expected tag 0x08, got 0x%02x", payload[0])
			}
			if (payload[1] != 0) != exists {
				t.Fatalf("got exists=%v, want %v", payload[1] != 0, exists)
			}
		})
	}
}

// TestControlSocketReadRequestWithFD verifies that reading a request
// doesn't lose an FD that arrives in the same recvmsg call.
func TestControlSocketReadRequestWithFD(t *testing.T) {
	srv, cli := socketPair(t)
	defer func() { _ = srv.Close() }()
	defer func() { _ = cli.Close() }()

	cs, _ := newControlSocket(srv)

	// Send request + FD back-to-back (like CRIU dump mode)
	go func() {
		sendProtoRequest(cli, "pages-1.img")
		r, w, _ := os.Pipe()
		_ = sendFD(cli, int(r.Fd()))
		_ = r.Close()
		_, _ = w.WriteString("hello")
		_ = w.Close()
	}()

	filename, err := cs.readRequest()
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	if filename != "pages-1.img" {
		t.Fatalf("filename = %q", filename)
	}

	fd, err := cs.recvFD()
	if err != nil {
		t.Fatalf("recvFD: %v", err)
	}

	f := os.NewFile(uintptr(fd), "pipe")
	data, _ := io.ReadAll(f)
	_ = f.Close()
	if string(data) != "hello" {
		t.Fatalf("pipe data = %q, want \"hello\"", data)
	}
}

// TestCaptureCompressesFiles verifies StartCapture with mock CRIU.
func TestCaptureCompressesFiles(t *testing.T) {
	dir := t.TempDir()

	result, err := StartCapture(dir)
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}

	socketPath := filepath.Join(dir, captureSocketName)
	waitForSocket(t, socketPath)
	conn := dialUnix(t, socketPath)

	testFiles := map[string][]byte{
		"core-1.img":  bytes.Repeat([]byte("ABCDEFGH"), 1024),
		"pages-1.img": bytes.Repeat([]byte{0x00}, 64*1024),
	}

	for name, data := range testFiles {
		sendProtoRequest(conn, name)
		r, w, _ := os.Pipe()
		_ = sendFD(conn, int(r.Fd()))
		_ = r.Close()
		_, _ = w.Write(data)
		_ = w.Close()
	}
	_ = conn.Close()

	res := <-result
	if res.Err != nil {
		t.Fatalf("capture error: %v", res.Err)
	}
	if res.Compression == nil {
		t.Fatal("compression info is nil")
	}
	if len(res.Compression.Files) != 2 {
		t.Fatalf("files = %d, want 2", len(res.Compression.Files))
	}

	// Verify round-trip decompression
	for name, data := range testFiles {
		f, _ := os.Open(filepath.Join(dir, name))
		dec, _ := io.ReadAll(lz4.NewReader(f))
		_ = f.Close()
		if !bytes.Equal(dec, data) {
			t.Fatalf("%s: decompressed mismatch (%d vs %d bytes)", name, len(dec), len(data))
		}
	}

	t.Logf("Compression: %d → %d bytes (%.1fx)",
		res.Compression.TotalOriginalSize,
		res.Compression.TotalCompressedSize,
		res.Compression.Ratio)
}

// TestServeDecompressesFiles verifies StartServe with mock CRIU.
func TestServeDecompressesFiles(t *testing.T) {
	dir := t.TempDir()

	testData := bytes.Repeat([]byte("TESTDATA"), 512)

	// Create compressed file on disk
	compression := &CompressionInfo{
		Algorithm: "lz4",
		Files:     map[string]CompressedFile{"core-1.img": {OriginalSize: int64(len(testData))}},
	}
	f, _ := os.Create(filepath.Join(dir, "core-1.img"))
	w := lz4.NewWriter(f)
	_, _ = w.Write(testData)
	_ = w.Close()
	_ = f.Close()

	done, err := StartServe(dir, compression)
	if err != nil {
		t.Fatalf("StartServe: %v", err)
	}

	socketPath := filepath.Join(dir, serveSocketName)
	waitForSocket(t, socketPath)
	conn := dialUnix(t, socketPath)

	// Request existing file
	sendProtoRequest(conn, "core-1.img")
	if !readProtoReply(t, conn) {
		t.Fatal("expected exists=true")
	}
	// Send pipe write-end (like CRIU restore does)
	r, pw, _ := os.Pipe()
	_ = sendFD(conn, int(pw.Fd()))
	_ = pw.Close()

	got, _ := io.ReadAll(r)
	_ = r.Close()
	if !bytes.Equal(got, testData) {
		t.Fatalf("data mismatch (%d vs %d)", len(got), len(testData))
	}

	// Request nonexistent file
	sendProtoRequest(conn, "nope.img")
	if readProtoReply(t, conn) {
		t.Fatal("expected exists=false")
	}

	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve error: %v", err)
	}
}

// TestCaptureServeRoundtrip does full compress→decompress round-trip.
func TestCaptureServeRoundtrip(t *testing.T) {
	dir := t.TempDir()

	// Capture
	capResult, _ := StartCapture(dir)
	waitForSocket(t, filepath.Join(dir, captureSocketName))
	conn := dialUnix(t, filepath.Join(dir, captureSocketName))

	original := bytes.Repeat([]byte("The quick brown fox. "), 5000)
	sendProtoRequest(conn, "pages-1.img")
	r, w, _ := os.Pipe()
	_ = sendFD(conn, int(r.Fd()))
	_ = r.Close()
	_, _ = w.Write(original)
	_ = w.Close()
	_ = conn.Close()

	res := <-capResult
	if res.Err != nil {
		t.Fatalf("capture: %v", res.Err)
	}

	// Serve
	serveDone, _ := StartServe(dir, res.Compression)
	waitForSocket(t, filepath.Join(dir, serveSocketName))
	conn2 := dialUnix(t, filepath.Join(dir, serveSocketName))

	sendProtoRequest(conn2, "pages-1.img")
	if !readProtoReply(t, conn2) {
		t.Fatal("expected exists=true")
	}
	pipeR, pipeW, _ := os.Pipe()
	_ = sendFD(conn2, int(pipeW.Fd()))
	_ = pipeW.Close()
	got, _ := io.ReadAll(pipeR)
	_ = pipeR.Close()
	_ = conn2.Close()

	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("roundtrip mismatch: %d vs %d bytes", len(got), len(original))
	}

	t.Logf("Roundtrip: %d → %d bytes (%.1fx)",
		res.Compression.TotalOriginalSize,
		res.Compression.TotalCompressedSize,
		res.Compression.Ratio)
}

// --- Helpers ---

func socketPair(t *testing.T) (c1, c2 *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	f1 := os.NewFile(uintptr(fds[0]), "s1")
	f2 := os.NewFile(uintptr(fds[1]), "s2")
	fc1, _ := net.FileConn(f1)
	fc2, _ := net.FileConn(f2)
	_ = f1.Close()
	_ = f2.Close()
	return fc1.(*net.UnixConn), fc2.(*net.UnixConn)
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("socket %s did not appear", path)
}

func dialUnix(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return conn
}

func sendProtoRequest(conn *net.UnixConn, filename string) {
	payload := append([]byte{0x0a, byte(len(filename))}, []byte(filename)...)
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(payload))) //nolint:gosec // bounded: payload is a small test protobuf
	_, _ = conn.Write(hdr[:])
	_, _ = conn.Write(payload)
}

func readProtoReply(t *testing.T, conn *net.UnixConn) bool {
	t.Helper()
	var hdr [4]byte
	_, _ = io.ReadFull(conn, hdr[:])
	payload := make([]byte, binary.LittleEndian.Uint32(hdr[:]))
	_, _ = io.ReadFull(conn, payload)
	if payload[0] != 0x08 {
		t.Fatalf("bad reply tag: 0x%02x", payload[0])
	}
	return payload[1] != 0
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
