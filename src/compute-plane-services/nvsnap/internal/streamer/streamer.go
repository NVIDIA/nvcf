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

// Package streamer implements CRIU's image streaming protocol for transparent
// checkpoint compression. It replaces the external criu-image-streamer binary
// with an in-process Go implementation that compresses with lz4 on-the-fly.
//
// Key design: ALL reads on the control socket use recvmsg() to prevent losing
// SCM_RIGHTS ancillary data. CRIU's dump mode sends filename + pipe FD
// back-to-back with no handshake, so Go's buffered Read() would silently
// discard the FD. We operate on the raw socket FD directly.
package streamer

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	captureSocketName = "streamer-capture.sock"
	serveSocketName   = "streamer-serve.sock"

	pipeBufSize = 1 << 20 // 1MB pipe buffer via F_SETPIPE_SZ
	copyBufSize = 1 << 20 // 1MB copy buffer for io.CopyBuffer
)

// Buffer pool to avoid GC pressure on the hot path.
var copyBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, copyBufSize)
		return &b
	},
}

func getCopyBuf() []byte  { return *copyBufPool.Get().(*[]byte) }
func putCopyBuf(b []byte) { copyBufPool.Put(&b) }

// CompressionInfo records compression metadata for a checkpoint.
type CompressionInfo struct {
	Algorithm           string                    `json:"algorithm"`
	Level               int                       `json:"level"`
	Files               map[string]CompressedFile `json:"files"`
	TotalOriginalSize   int64                     `json:"totalOriginalSize"`
	TotalCompressedSize int64                     `json:"totalCompressedSize"`
	Ratio               float64                   `json:"ratio"`
}

// CompressedFile records sizes for a single compressed file.
type CompressedFile struct {
	OriginalSize   int64 `json:"originalSize"`
	CompressedSize int64 `json:"compressedSize"`
}

// CaptureResult is returned when the capture streamer finishes.
type CaptureResult struct {
	Compression *CompressionInfo
	Err         error
}

// --- Raw control socket ---
//
// controlSocket wraps a Unix socket FD for the CRIU stream protocol.
// All reads use recvmsg() to correctly handle SCM_RIGHTS ancillary data.
// CRIU's dump mode sends filename request then pipe FD back-to-back
// without waiting, so we must never use Go's conn.Read() which would
// discard ancillary data.

type controlSocket struct {
	conn  *net.UnixConn
	rawFD int // extracted once, reused for all syscalls

	// Read buffer for protobuf data that arrives alongside an FD.
	// recvmsg may return data bytes + ancillary in one call.
	readBuf []byte
	readOff int

	// FD received as ancillary but not yet consumed by recvFD().
	pendingFD int
	hasPendFD bool
}

func newControlSocket(conn *net.UnixConn) (*controlSocket, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var rawFD int
	_ = rawConn.Control(func(fd uintptr) { rawFD = int(fd) })
	return &controlSocket{
		conn:      conn,
		rawFD:     rawFD,
		pendingFD: -1,
	}, nil
}

// oobSize is large enough for one FD via SCM_RIGHTS.
var oobSize = unix.CmsgSpace(int(unsafe.Sizeof(int32(0))))

// rawRecv does a single recvmsg, extracting any SCM_RIGHTS FD.
// Returns data bytes read and any FD (-1 if none).
// Blocks via Go's netpoller (retries on EAGAIN).
func (cs *controlSocket) rawRecv(buf []byte) (n, fd int, err error) {
	fd = -1
	oob := make([]byte, oobSize)

	rawConn, _ := cs.conn.SyscallConn()
	readErr := rawConn.Read(func(sockFD uintptr) bool {
		var oobn int
		n, oobn, _, _, err = unix.Recvmsg(int(sockFD), buf, oob, 0)
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			err = nil
			n = 0
			return false // retry via poller
		}
		if err != nil {
			return true
		}
		if n == 0 {
			err = io.EOF
			return true
		}
		// Extract FD from ancillary data if present
		if oobn > 0 {
			if msgs, parseErr := unix.ParseSocketControlMessage(oob[:oobn]); parseErr == nil {
				for _, msg := range msgs {
					if fds, fErr := unix.ParseUnixRights(&msg); fErr == nil && len(fds) > 0 {
						fd = fds[0]
					}
				}
			}
		}
		return true
	})
	if readErr != nil {
		return 0, -1, readErr
	}
	return n, fd, err
}

// readFull reads exactly len(buf) bytes from the control socket,
// saving any FDs that arrive as ancillary data along the way.
func (cs *controlSocket) readFull(buf []byte) error {
	// First, drain any buffered data from a previous over-read
	if len(cs.readBuf) > cs.readOff {
		n := copy(buf, cs.readBuf[cs.readOff:])
		cs.readOff += n
		if cs.readOff >= len(cs.readBuf) {
			cs.readBuf = nil
			cs.readOff = 0
		}
		if n == len(buf) {
			return nil
		}
		buf = buf[n:]
	}

	for len(buf) > 0 {
		n, fd, err := cs.rawRecv(buf)
		if err != nil {
			return err
		}
		if fd >= 0 {
			// Save FD for later recvFD() call
			if cs.hasPendFD {
				_ = unix.Close(cs.pendingFD) // shouldn't happen, but don't leak
			}
			cs.pendingFD = fd
			cs.hasPendFD = true
		}
		buf = buf[n:]
	}
	return nil
}

// readRequest reads a protobuf ImgStreamerRequestEntry.
// Returns filename, or io.EOF when CRIU disconnects.
func (cs *controlSocket) readRequest() (string, error) {
	var hdr [4]byte
	if err := cs.readFull(hdr[:]); err != nil {
		return "", err
	}
	size := binary.LittleEndian.Uint32(hdr[:])
	if size > 1<<20 {
		return "", fmt.Errorf("message too large: %d bytes", size)
	}
	buf := make([]byte, size)
	if err := cs.readFull(buf); err != nil {
		return "", fmt.Errorf("read payload: %w", err)
	}
	// Decode: tag 0x0a, varint length, string bytes
	if len(buf) < 2 || buf[0] != 0x0a {
		return "", fmt.Errorf("unexpected protobuf tag: 0x%02x", buf[0])
	}
	strLen := int(buf[1]) // filenames are < 128 bytes
	if len(buf) < 2+strLen {
		return "", fmt.Errorf("truncated filename")
	}
	return string(buf[2 : 2+strLen]), nil
}

// recvFD returns a pipe FD received via SCM_RIGHTS.
// If one was already received during readRequest/readFull, returns it immediately.
// Otherwise blocks until CRIU sends one.
func (cs *controlSocket) recvFD() (int, error) {
	if cs.hasPendFD {
		fd := cs.pendingFD
		cs.hasPendFD = false
		cs.pendingFD = -1
		return fd, nil
	}
	// Need to do another recvmsg to get the FD
	var dummy [1]byte
	_, fd, err := cs.rawRecv(dummy[:])
	if err != nil {
		return -1, err
	}
	if fd < 0 {
		return -1, fmt.Errorf("no FD in SCM_RIGHTS message")
	}
	return fd, nil
}

// writeReply writes an ImgStreamerReplyEntry (restore mode only).
func (cs *controlSocket) writeReply(exists bool) error {
	var val byte
	if exists {
		val = 0x01
	}
	payload := []byte{0x08, val}
	var msg [6]byte                                              // 4-byte header + 2-byte payload
	binary.LittleEndian.PutUint32(msg[:4], uint32(len(payload))) //nolint:gosec // bounded: payload is a fixed 2-byte slice
	copy(msg[4:], payload)
	_, err := cs.conn.Write(msg[:])
	return err
}

// --- Standalone helpers (used by tests and serve.go) ---

// sendFD sends a file descriptor over a Unix socket via SCM_RIGHTS.
func sendFD(conn *net.UnixConn, fd int) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("syscall conn: %w", err)
	}
	var sendErr error
	writeErr := rawConn.Write(func(sockFD uintptr) bool {
		rights := unix.UnixRights(fd)
		sendErr = unix.Sendmsg(int(sockFD), []byte{0}, rights, nil, 0)
		if sendErr == unix.EAGAIN || sendErr == unix.EWOULDBLOCK {
			sendErr = nil
			return false
		}
		return true
	})
	if writeErr != nil {
		return writeErr
	}
	if sendErr != nil {
		return fmt.Errorf("sendmsg: %w", sendErr)
	}
	return nil
}

// setPipeSize increases pipe buffer to reduce syscall overhead.
func setPipeSize(fd, size int) {
	_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETPIPE_SZ, size)
}
