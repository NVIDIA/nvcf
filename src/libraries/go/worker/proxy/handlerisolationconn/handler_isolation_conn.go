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
	"net"
	"net/http"
	"sync"
)

type HandlerPool struct {
	pool *sync.Pool
}

func NewHandlerPool(f func() http.Handler) HandlerPool {
	return HandlerPool{
		pool: &sync.Pool{
			New: func() any { return f() },
		},
	}
}

type HandlerConn struct {
	net.Conn
	release func()
	handler http.Handler
}

func NewHandlerConn(conn net.Conn, handlerPool HandlerPool) (HandlerConn, error) {
	releaseOnce := sync.Once{}
	handler := handlerPool.pool.Get().(http.Handler)
	return HandlerConn{Conn: conn, handler: handler, release: func() {
		releaseOnce.Do(func() {
			handlerPool.pool.Put(handler)
		})
	}}, nil
}

func (h HandlerConn) GetHandler() http.Handler {
	return h.handler
}

func (h HandlerConn) Close() error {
	defer h.release()
	return h.Conn.Close()
}

type closeWriter interface {
	CloseWrite() error
}

func (h HandlerConn) CloseWrite() error {
	if conn, ok := h.Conn.(closeWriter); ok {
		return conn.CloseWrite()
	}
	return nil
}
