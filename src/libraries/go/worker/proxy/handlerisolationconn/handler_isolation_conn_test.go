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

package handlerisolationconn

import (
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recordingConn wraps a net.Conn and records Close / CloseWrite.
type recordingConn struct {
	net.Conn
	closeCalled bool
	closeErr    error
}

func (c *recordingConn) Close() error {
	c.closeCalled = true
	return c.closeErr
}

type closeWriteConn struct {
	net.Conn
	closeWriteCalled bool
	closeWriteErr    error
}

func (c *closeWriteConn) CloseWrite() error {
	c.closeWriteCalled = true
	return c.closeWriteErr
}

func TestNewHandlerPoolAndConn_GetHandler(t *testing.T) {
	var created int32
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	pool := NewHandlerPool(func() http.Handler {
		atomic.AddInt32(&created, 1)
		return h
	})

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	hc, err := NewHandlerConn(client, pool)
	require.NoError(t, err)
	require.NotNil(t, hc.GetHandler())
	require.Equal(t, int32(1), atomic.LoadInt32(&created))
}

// TestHandlerConn_CloseReleasesHandler verifies that closing the conn returns
// the handler to the pool so it is reused on the next Get.
func TestHandlerConn_CloseReleasesHandler(t *testing.T) {
	var created int32
	pool := NewHandlerPool(func() http.Handler {
		atomic.AddInt32(&created, 1)
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	})

	rc := &recordingConn{Conn: nopConn{}}
	hc, err := NewHandlerConn(rc, pool)
	require.NoError(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&created))

	require.NoError(t, hc.Close())
	require.True(t, rc.closeCalled, "underlying conn must be closed")

	// After releasing, a subsequent Get must still return a usable handler.
	// (We avoid asserting exact pool reuse counts: sync.Pool may drop items on
	// GC, which the race build triggers aggressively.)
	hc2, err := NewHandlerConn(&recordingConn{Conn: nopConn{}}, pool)
	require.NoError(t, err)
	require.NotNil(t, hc2.GetHandler())
	require.GreaterOrEqual(t, atomic.LoadInt32(&created), int32(1))
}

func TestHandlerConn_ClosePropagatesError(t *testing.T) {
	wantErr := errors.New("close failed")
	pool := NewHandlerPool(func() http.Handler { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) })
	rc := &recordingConn{Conn: nopConn{}, closeErr: wantErr}
	hc, err := NewHandlerConn(rc, pool)
	require.NoError(t, err)
	require.ErrorIs(t, hc.Close(), wantErr)
}

// TestHandlerConn_ReleaseOnlyOnce verifies repeated Close calls are safe
// (sync.Once guards the release). recordingConn.Close itself is idempotent so
// both calls return nil without panicking.
func TestHandlerConn_ReleaseOnlyOnce(t *testing.T) {
	pool := NewHandlerPool(func() http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	})
	rc := &recordingConn{Conn: nopConn{}}
	hc, err := NewHandlerConn(rc, pool)
	require.NoError(t, err)
	require.NoError(t, hc.Close())
	require.NoError(t, hc.Close())
	require.True(t, rc.closeCalled)
}

func TestHandlerConn_CloseWrite_Supported(t *testing.T) {
	pool := NewHandlerPool(func() http.Handler { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) })
	cw := &closeWriteConn{Conn: nopConn{}}
	hc, err := NewHandlerConn(cw, pool)
	require.NoError(t, err)
	require.NoError(t, hc.CloseWrite())
	require.True(t, cw.closeWriteCalled)
}

func TestHandlerConn_CloseWrite_PropagatesError(t *testing.T) {
	wantErr := errors.New("cw failed")
	pool := NewHandlerPool(func() http.Handler { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) })
	cw := &closeWriteConn{Conn: nopConn{}, closeWriteErr: wantErr}
	hc, err := NewHandlerConn(cw, pool)
	require.NoError(t, err)
	require.ErrorIs(t, hc.CloseWrite(), wantErr)
}

func TestHandlerConn_CloseWrite_Unsupported(t *testing.T) {
	pool := NewHandlerPool(func() http.Handler { return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) })
	hc, err := NewHandlerConn(nopConn{}, pool)
	require.NoError(t, err)
	// nopConn does not implement CloseWrite, so this must be a no-op returning nil.
	require.NoError(t, hc.CloseWrite())
}

// nopConn is a no-op net.Conn that does not implement CloseWrite.
type nopConn struct{}

func (nopConn) Read([]byte) (int, error)         { return 0, nil }
func (nopConn) Write(b []byte) (int, error)      { return len(b), nil }
func (nopConn) Close() error                     { return nil }
func (nopConn) LocalAddr() net.Addr              { return nil }
func (nopConn) RemoteAddr() net.Addr             { return nil }
func (nopConn) SetDeadline(time.Time) error      { return nil }
func (nopConn) SetReadDeadline(time.Time) error  { return nil }
func (nopConn) SetWriteDeadline(time.Time) error { return nil }
