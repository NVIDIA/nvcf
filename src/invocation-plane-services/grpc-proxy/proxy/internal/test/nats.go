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
	"io"
	"net"
	"strings"
	"time"
)

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
// the script are accepted.
type FakeNatsServer struct {
	URL      string
	listener net.Listener
}

func NewFakeNatsServer(script ...NatsAction) (*FakeNatsServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for i := 0; ; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			action := NatsAccept
			if i < len(script) {
				action = script[i]
			}
			go serveFakeNats(conn, action)
		}
	}()
	return &FakeNatsServer{URL: "nats://" + listener.Addr().String(), listener: listener}, nil
}

func (s *FakeNatsServer) Close() error {
	return s.listener.Close()
}

func serveFakeNats(conn net.Conn, action NatsAction) {
	defer conn.Close()
	info := `INFO {"server_id":"fake","version":"2.10.0","proto":1,"max_payload":1048576,"auth_required":true}` + "\r\n"
	if _, err := io.WriteString(conn, info); err != nil {
		return
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "PING") {
			continue
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
