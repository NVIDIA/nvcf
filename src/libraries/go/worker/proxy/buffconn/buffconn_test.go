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

package buffconn

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// closeWriteConn wraps net.Conn and records CloseWrite calls.
type closeWriteConn struct {
	net.Conn
	closeWriteCalled bool
	closeWriteErr    error
}

func (c *closeWriteConn) CloseWrite() error {
	c.closeWriteCalled = true
	return c.closeWriteErr
}

// TestNewBufConn_NoBufferedData verifies that when the bufio.Reader has no
// buffered data the raw connection is returned unwrapped.
func TestNewBufConn_NoBufferedData(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	br := bufio.NewReader(client)
	got := NewBufConn(client, br)
	require.Same(t, client, got, "expected the raw conn to be returned when nothing is buffered")
}

// TestNewBufConn_WithBufferedData verifies that buffered data is drained first
// and then reads fall through to the underlying conn.
func TestNewBufConn_WithBufferedData(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	// Feed bytes into the pipe so the bufio.Reader can buffer them.
	go func() {
		_, _ = server.Write([]byte("buffered"))
	}()

	br := bufio.NewReader(client)
	// Force the reader to buffer at least one byte.
	first, err := br.ReadByte()
	require.NoError(t, err)
	require.Equal(t, byte('b'), first)
	require.Greater(t, br.Buffered(), 0)

	c := NewBufConn(client, br)
	// Not the raw conn since data is buffered.
	require.NotSame(t, client, c)

	// First read drains the remaining buffered bytes ("uffered").
	buf := make([]byte, 100)
	n, err := c.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "uffered", string(buf[:n]))

	// Next read should fall through to the underlying conn. Push more data and
	// close the write side so Read returns.
	go func() {
		_, _ = server.Write([]byte("direct"))
		_ = server.Close()
	}()
	rest, err := io.ReadAll(c)
	require.NoError(t, err)
	require.Equal(t, "direct", string(rest))
}

// TestBufConn_ReadShortBuffer verifies the n < len(p) truncation branch where
// the destination buffer is larger than the buffered byte count.
func TestBufConn_ReadShortBuffer(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = server.Write([]byte("ab"))
	}()

	br := bufio.NewReader(client)
	_, err := br.Peek(1) // buffer the two bytes
	require.NoError(t, err)
	require.Equal(t, 2, br.Buffered())

	c := NewBufConn(client, br)
	// Pass a buffer larger than what is buffered. Read should only return the
	// buffered bytes (n < len(p) branch truncates p to the buffered count).
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	require.NoError(t, err)
	require.LessOrEqual(t, n, 2)
	require.Equal(t, "ab"[:n], string(buf[:n]))
}

// TestBufConn_CloseWrite_Supported verifies CloseWrite is forwarded when the
// underlying conn supports it.
func TestBufConn_CloseWrite_Supported(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() { _, _ = server.Write([]byte("xy")) }()
	br := bufio.NewReader(client)
	_, _ = br.Peek(1)

	cw := &closeWriteConn{Conn: client}
	c := NewBufConn(cw, br)

	type closeWriter interface{ CloseWrite() error }
	wc, ok := c.(closeWriter)
	require.True(t, ok)
	require.NoError(t, wc.CloseWrite())
	require.True(t, cw.closeWriteCalled)
}

// TestBufConn_CloseWrite_Unsupported verifies CloseWrite returns nil when the
// underlying conn does not implement CloseWrite.
func TestBufConn_CloseWrite_Unsupported(t *testing.T) {
	r := bytes.NewReader([]byte("zz"))
	noCloseWrite := &plainConn{Reader: r}
	br := bufio.NewReader(r)
	_, _ = br.Peek(1)

	c := NewBufConn(noCloseWrite, br)
	type closeWriter interface{ CloseWrite() error }
	wc, ok := c.(closeWriter)
	require.True(t, ok)
	require.NoError(t, wc.CloseWrite())
}

// plainConn is a minimal net.Conn that does NOT implement CloseWrite.
type plainConn struct {
	io.Reader
}

func (p *plainConn) Write(b []byte) (int, error)        { return len(b), nil }
func (p *plainConn) Close() error                       { return nil }
func (p *plainConn) LocalAddr() net.Addr                { return fakeAddr{} }
func (p *plainConn) RemoteAddr() net.Addr               { return fakeAddr{} }
func (p *plainConn) SetDeadline(t time.Time) error      { return nil }
func (p *plainConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *plainConn) SetWriteDeadline(t time.Time) error { return nil }

type fakeAddr struct{}

func (fakeAddr) Network() string { return "fake" }
func (fakeAddr) String() string  { return "fake" }
