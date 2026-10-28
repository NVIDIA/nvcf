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

package utils

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetWriteBufferSizeRealConn(t *testing.T) {
	// Loopback listener on an ephemeral port (:0) so no fixed port is bound.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	// A real *net.TCPConn implements syscall.Conn, so the SO_SNDBUF path runs.
	size := GetWriteBufferSize(conn)
	require.Greater(t, size, 0, "kernel should report a positive socket send buffer")
}

// nonSyscallConn implements net.Conn but NOT syscall.Conn, exercising the
// fallback return-0 path.
type nonSyscallConn struct{ net.Conn }

func TestGetWriteBufferSizeNonSyscallConn(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	// net.Pipe conns do not implement syscall.Conn.
	require.Equal(t, 0, GetWriteBufferSize(nonSyscallConn{Conn: left}))
}
