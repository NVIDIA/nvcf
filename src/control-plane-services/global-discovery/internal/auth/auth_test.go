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

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fixed struct {
	p   Principal
	err error
}

func (f fixed) Authenticate(context.Context, string) (Principal, error) { return f.p, f.err }

type stream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s stream) Context() context.Context { return s.ctx }

func TestNoop(t *testing.T) {
	p, err := NoopAuthenticator{}.Authenticate(context.Background(), "/x")
	require.NoError(t, err)
	assert.Equal(t, Anonymous, p)
	_, ok := FromContext(context.Background())
	assert.False(t, ok)
}

func TestUnary(t *testing.T) {
	bound := Principal{Subject: "controller", ClusterSet: "prod", Cluster: "a"}
	var got Principal
	handler := func(ctx context.Context, _ any) (any, error) {
		got, _ = FromContext(ctx)
		return "ok", nil
	}
	ic := UnaryServerInterceptor(fixed{p: bound}, DefaultExempt...)
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/nvidia.gds.v1.PoolService/ListPools"}, handler)
	require.NoError(t, err)
	assert.Equal(t, bound, got)

	ic = UnaryServerInterceptor(fixed{err: errors.New("bad token")}, DefaultExempt...)
	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/nvidia.gds.v1.PoolService/ListPools"}, handler)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	ic = UnaryServerInterceptor(fixed{err: status.Error(codes.PermissionDenied, "no")}, DefaultExempt...)
	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/nvidia.gds.v1.PoolService/ListPools"}, handler)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	got = Principal{}
	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, handler)
	require.NoError(t, err, "health checks are exempt")
	assert.Equal(t, Principal{}, got)
}

func TestStream(t *testing.T) {
	var got Principal
	handler := func(_ any, ss grpc.ServerStream) error {
		got, _ = FromContext(ss.Context())
		return nil
	}
	ss := stream{ctx: context.Background()}
	ic := StreamServerInterceptor(NoopAuthenticator{}, DefaultExempt...)
	require.NoError(t, ic(nil, ss, &grpc.StreamServerInfo{FullMethod: "/nvidia.gds.v1.PoolService/WatchPools"}, handler))
	assert.True(t, got.Anonymous)

	ic = StreamServerInterceptor(fixed{err: errors.New("no")}, DefaultExempt...)
	err := ic(nil, ss, &grpc.StreamServerInfo{FullMethod: "/nvidia.gds.v1.PoolService/WatchPools"}, handler)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	require.NoError(t, ic(nil, ss, &grpc.StreamServerInfo{FullMethod: "/grpc.health.v1.Health/Watch"}, handler))
}
