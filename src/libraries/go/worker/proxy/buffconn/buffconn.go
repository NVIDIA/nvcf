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
	"net"
)

// NewBufConn takes from golang.org/x/net/http2/h2c but uses a Reader only
func NewBufConn(conn net.Conn, br *bufio.Reader) net.Conn {
	if br.Buffered() == 0 {
		// If there's no buffered data to be read,
		// we can just discard the bufio.Reader.
		return conn
	}
	return &bufConn{conn, br}
}

// bufConn wraps a net.Conn, but reads drain the bufio.Reader first.
type bufConn struct {
	net.Conn
	*bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) {
	if c.Reader == nil {
		return c.Conn.Read(p)
	}
	n := c.Buffered()
	if n == 0 {
		c.Reader = nil
		return c.Conn.Read(p)
	}
	if n < len(p) {
		p = p[:n]
	}
	return c.Reader.Read(p)
}

type closeWriter interface {
	CloseWrite() error
}

func (c *bufConn) CloseWrite() error {
	if conn, ok := c.Conn.(closeWriter); ok {
		return conn.CloseWrite()
	}
	return nil
}
