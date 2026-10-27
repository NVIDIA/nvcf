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

package server

import (
	"context"
	"fmt"

	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// zapLogger adapts zap to the go-grpc-middleware logging interface.
func zapLogger(l *zap.Logger) logging.Logger {
	return logging.LoggerFunc(func(_ context.Context, lvl logging.Level, msg string, fields ...any) {
		zf := make([]zap.Field, 0, len(fields)/2)
		for i := 0; i+1 < len(fields); i += 2 {
			key, ok := fields[i].(string)
			if !ok {
				key = fmt.Sprint(fields[i])
			}
			zf = append(zf, zap.Any(key, fields[i+1]))
		}
		if ce := l.Check(zapLevel(lvl), msg); ce != nil {
			ce.Write(zf...)
		}
	})
}

func zapLevel(lvl logging.Level) zapcore.Level {
	switch {
	case lvl >= logging.LevelError:
		return zapcore.ErrorLevel
	case lvl >= logging.LevelWarn:
		return zapcore.WarnLevel
	case lvl >= logging.LevelInfo:
		return zapcore.InfoLevel
	}
	return zapcore.DebugLevel
}

// codeToLevel logs successful calls at debug (heartbeats are frequent),
// expected client errors at info, server-side trouble at warn and failures
// that need attention at error.
func codeToLevel(code codes.Code) logging.Level {
	switch code {
	case codes.OK:
		return logging.LevelDebug
	case codes.Canceled, codes.InvalidArgument, codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition,
		codes.Aborted, codes.OutOfRange, codes.Unauthenticated, codes.PermissionDenied:
		return logging.LevelInfo
	case codes.DeadlineExceeded, codes.ResourceExhausted, codes.Unavailable:
		return logging.LevelWarn
	}
	return logging.LevelError
}

// requestFields are the request fields logged with every unary call.
type requestFields interface {
	GetClusterset() string
}

// fieldsFromRequest returns the clusterset, cluster, namespace, name and
// pool id of a request, when it has them, and the x-request-id header.
func fieldsFromRequest(ctx context.Context, req any) logging.Fields {
	var f logging.Fields
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if ids := md.Get("x-request-id"); len(ids) > 0 {
			f = append(f, "request_id", ids[0])
		}
	}
	if r, ok := req.(requestFields); ok && r.GetClusterset() != "" {
		f = append(f, "clusterset", r.GetClusterset())
	}
	if r, ok := req.(interface{ GetCluster() string }); ok && r.GetCluster() != "" {
		f = append(f, "cluster", r.GetCluster())
	}
	if r, ok := req.(interface{ GetNamespace() string }); ok && r.GetNamespace() != "" {
		f = append(f, "namespace", r.GetNamespace())
	}
	if r, ok := req.(interface{ GetName() string }); ok && r.GetName() != "" {
		f = append(f, "name", r.GetName())
	}
	if r, ok := req.(interface{ GetPoolId() string }); ok && r.GetPoolId() != "" {
		f = append(f, "pool_id", r.GetPoolId())
	}
	return f
}

// requestFieldsUnary injects the request fields for the logging
// interceptor, which must run inside it.
func requestFieldsUnary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if f := fieldsFromRequest(ctx, req); len(f) > 0 {
		logging.AddFields(ctx, f)
	}
	return handler(ctx, req)
}
