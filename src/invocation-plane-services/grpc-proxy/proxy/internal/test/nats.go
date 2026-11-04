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
package test

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// FakeNatsToken is the only auth token FakeNatsServer accepts.
const FakeNatsToken = "token"

// NatsAction is how FakeNatsServer answers one client connection.
type NatsAction int

const (
	// NatsAccept accepts the CONNECT and keeps answering PINGs.
	NatsAccept NatsAction = iota
	// NatsAcceptThenDrop accepts the CONNECT, then closes the connection so the
	// client has to reconnect.
	NatsAcceptThenDrop
	// NatsRejectAuth answers the CONNECT with an authorization violation, as a
	// server does when its auth callout is unavailable.
	NatsRejectAuth
)

// FakeNatsServer speaks just enough of the NATS client protocol to accept or
// reject a CONNECT. Connection i follows script[i]; connections past the end of
// the script are accepted. A CONNECT without FakeNatsToken is always rejected.
type FakeNatsServer struct {
	URL      string
	listener net.Listener

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

func NewFakeNatsServer(script ...NatsAction) (*FakeNatsServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &FakeNatsServer{URL: "nats://" + listener.Addr().String(), listener: listener}
	go func() {
		for i := 0; ; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if !s.track(conn) {
				return
			}
			action := NatsAccept
			if i < len(script) {
				action = script[i]
			}
			go serveFakeNats(conn, action)
		}
	}()
	return s, nil
}

func (s *FakeNatsServer) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = conn.Close()
		return false
	}
	s.conns = append(s.conns, conn)
	return true
}

// Close stops listening and drops every open client connection, so a client
// can neither stay connected nor reconnect.
func (s *FakeNatsServer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	return s.listener.Close()
}

func serveFakeNats(conn net.Conn, action NatsAction) {
	defer conn.Close()
	info := `INFO {"server_id":"fake","version":"2.10.0","proto":1,"max_payload":1048576,"auth_required":true}` + "\r\n"
	if _, err := io.WriteString(conn, info); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
	authorized := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if connect, ok := strings.CutPrefix(line, "CONNECT "); ok {
			var options struct {
				AuthToken string `json:"auth_token"`
			}
			authorized = json.Unmarshal([]byte(connect), &options) == nil && options.AuthToken == FakeNatsToken
			continue
		}
		if !strings.HasPrefix(line, "PING") {
			continue
		}
		if !authorized {
			action = NatsRejectAuth
		}
		switch action {
		case NatsRejectAuth:
			_, _ = io.WriteString(conn, "-ERR 'Authorization Violation'\r\n")
			return
		case NatsAcceptThenDrop:
			_, _ = io.WriteString(conn, "PONG\r\n")
			// Let the client finish connecting before the drop.
			time.Sleep(50 * time.Millisecond)
			return
		default:
			_, _ = io.WriteString(conn, "PONG\r\n")
		}
	}
}
