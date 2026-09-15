/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
	"github.com/quic-go/quic-go/http3"
	"net"
	"strings"
)

var _ net.Conn = (*Conn)(nil)

func NewQuicStreamConn(conn http3.HTTPStreamer) *Conn {
	return &Conn{Stream: conn.HTTPStream()}
}

type Conn struct {
	*http3.Stream
}

func (c *Conn) LocalAddr() net.Addr {
	return fakeAddr("http3-stream-local")
}

func (c *Conn) RemoteAddr() net.Addr {
	return fakeAddr("http3-stream-remote")
}

func (c *Conn) CloseWrite() error {
	// close on quic streams is CloseWrite
	return c.Close()
}

func (c *Conn) Close() error {
	err := c.Stream.Close()
	if err != nil {
		if strings.HasPrefix(err.Error(), "close called for canceled stream") {
			return nil
		}
	}
	return err
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }
