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
	"io"
	"net"
	"testing"
	"time"

	"github.com/hellofresh/health-go/v5"
	"golang.org/x/net/http2"
)

func TestHTTP2ServerAdvertisesBoundedConcurrentStreams(t *testing.T) {
	healthManager, err := health.New()
	if err != nil {
		t.Fatalf("health.New() failed: %v", err)
	}

	server := createHttp2Server(&StreamDirector{}, "", healthManager)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() failed: %v", err)
	}
	go server.Serve(listener)
	t.Cleanup(func() {
		_ = server.Close()
	})

	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("net.DialTimeout() failed: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() failed: %v", err)
	}
	if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
		t.Fatalf("writing HTTP/2 client preface failed: %v", err)
	}

	framer := http2.NewFramer(conn, conn)
	if err := framer.WriteSettings(); err != nil {
		t.Fatalf("writing client SETTINGS failed: %v", err)
	}
	for {
		frame, err := framer.ReadFrame()
		if err != nil {
			t.Fatalf("reading server frame failed: %v", err)
		}
		settings, ok := frame.(*http2.SettingsFrame)
		if !ok || settings.IsAck() {
			continue
		}
		streams, ok := settings.Value(http2.SettingMaxConcurrentStreams)
		if !ok {
			t.Fatal("server SETTINGS omitted MAX_CONCURRENT_STREAMS")
		}
		if streams != maxConcurrentStreams {
			t.Fatalf("MAX_CONCURRENT_STREAMS = %d, want %d", streams, maxConcurrentStreams)
		}
		return
	}
}
