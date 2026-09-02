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

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"ratelimiter/pb"
)

type blockingRateLimitServer struct {
	pb.UnimplementedRateLimitServiceServer
	entered chan struct{}
	release chan struct{}
}

func (s *blockingRateLimitServer) RateLimit(ctx context.Context, _ *pb.RateLimitRequest) (*pb.RateLimitResponse, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return &pb.RateLimitResponse{}, nil
}

// Returns a running server with one RateLimit call in flight.
func startBlockedServer(t *testing.T) (*grpc.Server, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	stub := &blockingRateLimitServer{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	server := grpc.NewServer()
	pb.RegisterRateLimitServiceServer(server, stub)
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _ = pb.NewRateLimitServiceClient(conn).RateLimit(context.Background(), &pb.RateLimitRequest{})
	}()

	select {
	case <-stub.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("handler never received the call")
	}

	return server, func() {
		close(stub.release)
		_ = conn.Close()
		<-callDone
	}
}

// An unbounded drain delays the Olric leave until the pod is SIGKILLed.
func TestStopGrpcServerBoundedByDrainTimeout(t *testing.T) {
	server, cleanup := startBlockedServer(t)
	defer cleanup()

	drainTimeout := 200 * time.Millisecond
	returned := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		stopGrpcServer(server, drainTimeout)
		returned <- time.Since(start)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatalf("stopGrpcServer still blocked on an in-flight call, want it bounded near %v", drainTimeout)
	}
}

func TestStopGrpcServerReturnsEarlyWhenIdle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	pb.RegisterRateLimitServiceServer(server, &blockingRateLimitServer{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	})
	go func() { _ = server.Serve(listener) }()

	returned := make(chan struct{})
	go func() {
		stopGrpcServer(server, 10*time.Second)
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("idle shutdown did not return promptly")
	}
}
