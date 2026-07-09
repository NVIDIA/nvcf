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
package proxy

import (
	"net"
	"testing"
	"time"
)

// addrConn is a minimal net.Conn whose RemoteAddr is configurable for testing.
type addrConn struct {
	remote net.Addr
}

func (c *addrConn) Read(b []byte) (int, error)         { return 0, nil }
func (c *addrConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *addrConn) Close() error                       { return nil }
func (c *addrConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *addrConn) RemoteAddr() net.Addr               { return c.remote }
func (c *addrConn) SetDeadline(t time.Time) error      { return nil }
func (c *addrConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *addrConn) SetWriteDeadline(t time.Time) error { return nil }

// stubAddr is a net.Addr with a configurable network and string so tests can
// distinguish a real transport peer from the "fake"-network placeholder that
// quicconn reports for HTTP/3 streams.
type stubAddr struct {
	network string
	addr    string
}

func (a stubAddr) Network() string { return a.network }
func (a stubAddr) String() string  { return a.addr }

func TestNetworkPeerAddress(t *testing.T) {
	tests := []struct {
		name   string
		remote net.Addr
		want   string
		wantOK bool
	}{
		{
			name:   "tcp addr strips port",
			remote: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 8443},
			want:   "10.1.2.3",
			wantOK: true,
		},
		{
			name:   "host:port string strips port",
			remote: stubAddr{network: "tcp", addr: "10.4.5.6:1234"},
			want:   "10.4.5.6",
			wantOK: true,
		},
		{
			// quicconn.Conn.RemoteAddr() returns a placeholder on the "fake"
			// network; it carries no peer identity so it must be omitted, not
			// stamped onto the span.
			name:   "quic placeholder is unavailable",
			remote: stubAddr{network: "fake", addr: "http3-stream-remote"},
			want:   "",
			wantOK: false,
		},
		{
			name:   "nil remote addr is unavailable",
			remote: nil,
			want:   "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := networkPeerAddress(&addrConn{remote: tt.remote})
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("networkPeerAddress() = (%q, %t), want (%q, %t)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
