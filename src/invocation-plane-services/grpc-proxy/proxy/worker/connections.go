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
package worker

import (
	"context"
	"fmt"
	"net"
	"nvcf-grpc-proxy/proxy/metrics"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

const TcpSessionSpanName = "tcp session"

type functionRoutingKey struct {
	functionId        string
	functionVersionId string // may be empty
}

type workerConnectionWithInit struct {
	workerConnection *WorkerConnection // can be nil, even after init
	err              error
	init             sync.Once
}

// getWorkerConnection can return nil
func (c *workerConnectionWithInit) getWorkerConnection(maybeInit func() (*WorkerConnection, error)) (*WorkerConnection, error) {
	c.init.Do(func() {
		c.workerConnection, c.err = maybeInit()
	})
	return c.workerConnection, c.err
}

type ConnectionTrackingConn struct {
	net.Conn

	span      trace.Span
	initSpan  sync.Once
	closeOnce sync.Once

	// one connection may reach out to multiple workers by invoking different functions
	workerConnectionLock sync.Mutex
	workerConnections    map[functionRoutingKey]*workerConnectionWithInit
}

func NewConnectionTrackingConn(conn net.Conn) *ConnectionTrackingConn {
	metrics.ActiveClientConnectionsTotal.Inc()
	tracer := otel.GetTracerProvider().Tracer("connection-tracer")
	_, span := tracer.Start(context.Background(), TcpSessionSpanName, trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer))
	return &ConnectionTrackingConn{Conn: conn, span: span}
}

func (c *ConnectionTrackingConn) InitStatefulSession(ctx context.Context) {
	c.initSpan.Do(func() {
		c.span.SetName("stateful session")
		c.setParentSpan(ctx)
	})
}

// InitWorkerConn may return nil if the worker connection for this function id and version id was already closed
func (c *ConnectionTrackingConn) InitWorkerConn(functionId, functionVersionId string, maybeInit func() (*WorkerConnection, error)) (*WorkerConnection, error) {
	c.workerConnectionLock.Lock()
	defer c.workerConnectionLock.Unlock()
	key := functionRoutingKey{
		functionId:        functionId,
		functionVersionId: functionVersionId,
	}
	workerConn, ok := c.workerConnections[key]
	// make a new connection and init if needed
	if !ok {
		// deferred map creation so we don't allocate a map if we get non-worker traffic, like health checks
		if c.workerConnections == nil {
			// most of the time it will just be the one function, so don't waste space
			c.workerConnections = make(map[functionRoutingKey]*workerConnectionWithInit, 1)
		}
		workerConn = &workerConnectionWithInit{}
		c.workerConnections[key] = workerConn
	}

	connection, err := workerConn.getWorkerConnection(maybeInit)
	// either means this ConnectionTrackingConn was closed,
	// or the NVCF API failed to return from the proxy worker invocation call
	if err != nil {
		// reset the mapping in case the client comes back and wants to try
		// invoking the NVCF API again with different creds but don't retry inline
		c.workerConnections[key] = &workerConnectionWithInit{}
		return nil, err
	}

	// if we have a worker connection entry but the worker side was already closed
	// then get a fresh entry
	if connection.WorkerClosed() {
		zap.L().Debug("worker entry already closed, removing worker connection reference from client connection")
		workerConn = &workerConnectionWithInit{}
		c.workerConnections[key] = workerConn
		connection, err = workerConn.getWorkerConnection(maybeInit)
		// pass back any error with the fresh connection
		if err != nil {
			// reset the mapping in case the client comes back and wants to try
			// invoking the NVCF API again with different creds but don't retry inline
			c.workerConnections[key] = &workerConnectionWithInit{}
			return nil, err
		}
	}
	return connection, err
}

func (c *ConnectionTrackingConn) Close() error {
	defer c.closeOnce.Do(func() {
		c.span.End()
		metrics.ActiveClientConnectionsTotal.Dec()
	})

	zap.L().Debug("closing client connection",
		zap.String("ptr", fmt.Sprintf("%p", c)),
		zap.Int("active_function_connections", len(c.workerConnections)))

	c.workerConnectionLock.Lock()
	defer c.workerConnectionLock.Unlock()

	for key, conn := range c.workerConnections {
		workerConnection, _ := conn.getWorkerConnection(func() (*WorkerConnection, error) {
			return nil, fmt.Errorf("worker connection force closed due to closing client connection")
		})
		if workerConnection != nil {
			zap.L().Info("triggering worker connection shutdown from client close",
				zap.String("function_id", key.functionId),
				zap.String("function_version_id", key.functionVersionId),
				zap.Stringer("request_id", workerConnection.RequestId))
			workerConnection.onInactive() // this will trigger the worker connection to shut down
		}
	}
	return c.Conn.Close()
}

type ServerConnectionKey string

const serverConnectionKey = ServerConnectionKey("connection")

func ConnFromCtx(ctx context.Context) *ConnectionTrackingConn {
	return ctx.Value(serverConnectionKey).(*ConnectionTrackingConn)
}

func CtxWithConn(ctx context.Context, conn *ConnectionTrackingConn) context.Context {
	return context.WithValue(ctx, serverConnectionKey, conn)
}
