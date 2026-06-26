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

package quicconn

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeStream is an in-memory quicStream implementation.
type fakeStream struct {
	readData     []byte
	readErr      error
	written      []byte
	closeErr     error
	closeCalled  bool
	deadline     time.Time
	readDeadline time.Time
	writeDline   time.Time
}

func (f *fakeStream) Read(b []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	n := copy(b, f.readData)
	f.readData = f.readData[n:]
	return n, nil
}

func (f *fakeStream) Write(b []byte) (int, error) {
	f.written = append(f.written, b...)
	return len(b), nil
}

func (f *fakeStream) Close() error {
	f.closeCalled = true
	return f.closeErr
}

func (f *fakeStream) SetDeadline(t time.Time) error      { f.deadline = t; return nil }
func (f *fakeStream) SetReadDeadline(t time.Time) error  { f.readDeadline = t; return nil }
func (f *fakeStream) SetWriteDeadline(t time.Time) error { f.writeDline = t; return nil }

func TestConn_Addrs(t *testing.T) {
	c := NewHttp3StreamConn(&fakeStream{})
	require.Equal(t, "http3-stream-local", c.LocalAddr().String())
	require.Equal(t, "fake", c.LocalAddr().Network())
	require.Equal(t, "http3-stream-remote", c.RemoteAddr().String())
	require.Equal(t, "fake", c.RemoteAddr().Network())
}

func TestConn_ReadWrite(t *testing.T) {
	fs := &fakeStream{readData: []byte("hello")}
	c := NewHttp3StreamConn(fs)

	buf := make([]byte, 5)
	n, err := c.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "hello", string(buf[:n]))

	n, err = c.Write([]byte("world"))
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, "world", string(fs.written))
}

func TestConn_Deadlines(t *testing.T) {
	fs := &fakeStream{}
	c := NewHttp3StreamConn(fs)
	now := time.Now()
	require.NoError(t, c.SetDeadline(now))
	require.NoError(t, c.SetReadDeadline(now.Add(time.Second)))
	require.NoError(t, c.SetWriteDeadline(now.Add(2*time.Second)))
	require.Equal(t, now, fs.deadline)
	require.Equal(t, now.Add(time.Second), fs.readDeadline)
	require.Equal(t, now.Add(2*time.Second), fs.writeDline)
}

func TestConn_CloseWrite_DelegatesToClose(t *testing.T) {
	fs := &fakeStream{}
	c := NewHttp3StreamConn(fs)
	require.NoError(t, c.CloseWrite())
	require.True(t, fs.closeCalled)
}

func TestConn_Close_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	fs := &fakeStream{closeErr: wantErr}
	c := NewHttp3StreamConn(fs)
	err := c.Close()
	require.ErrorIs(t, err, wantErr)
}

// TestConn_Close_SwallowsCanceledStreamError verifies that the specific
// "close called for canceled stream" error is treated as a clean close.
func TestConn_Close_SwallowsCanceledStreamError(t *testing.T) {
	fs := &fakeStream{closeErr: errors.New("close called for canceled stream 7")}
	c := NewHttp3StreamConn(fs)
	require.NoError(t, c.Close())
}

func TestConn_Close_NoError(t *testing.T) {
	fs := &fakeStream{}
	c := NewHttp3StreamConn(fs)
	require.NoError(t, c.Close())
	require.True(t, fs.closeCalled)
}
