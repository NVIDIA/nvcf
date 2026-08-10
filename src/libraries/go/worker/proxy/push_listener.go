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

package proxy

import (
	"net"
	"sync"
)

type PushListener struct {
	ch     chan net.Conn
	closed bool
	mu     sync.Mutex
}

func NewPushListener() *PushListener {
	return &PushListener{
		ch: make(chan net.Conn),
	}
}

func (l *PushListener) Accept() (net.Conn, error) {
	conn, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *PushListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return net.ErrClosed
	}
	l.closed = true
	close(l.ch)
	return nil
}

type waitForCloseConn struct {
	net.Conn
	closeOnce sync.Once
	closed    chan struct{}
}

func (c *waitForCloseConn) Close() error {
	defer c.closeOnce.Do(func() {
		close(c.closed)
	})
	return c.Conn.Close()
}

func (c *waitForCloseConn) Unwrap() net.Conn {
	return c.Conn
}

func (l *PushListener) ServeConn(conn net.Conn) error {
	// lock to ensure we don't push connections into a closed listener
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	waitableConn := &waitForCloseConn{
		Conn:   conn,
		closed: make(chan struct{}),
	}
	l.ch <- waitableConn
	// now that the connection is pushed, we can unlock
	l.mu.Unlock()
	// wait for the connection to be finished processing, outside the global listener lock
	// keeping the lock held while waiting would cause only one connection to be served at a time
	<-waitableConn.closed
	return nil
}

func (l *PushListener) Addr() net.Addr {
	return &net.IPAddr{IP: net.IPv4(127, 0, 0, 1)}
}
