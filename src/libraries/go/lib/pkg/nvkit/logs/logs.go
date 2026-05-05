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

package logs

import (
	"context"
	"os"
	"time"

	"github.com/go-kit/kit/log" //nolint:staticcheck
	kitzap "github.com/go-kit/kit/log/zap"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/api/errors/v1"
	serverutils "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/servers/utils"
)

const (
	KeyTraceID   = "trace_id"
	KeyRequestID = "request_id"
	KeyErrorID   = "error_id"
)

// StandardLogger returns a base logger
func StandardLogger() log.Logger {
	logger := log.NewLogfmtLogger(os.Stderr)
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	logger = log.With(logger, "caller", log.DefaultCaller)

	return logger
}

// ZapLogger is go-kit and zap compatible logger
type ZapLogger struct {
	kitLogger log.Logger
	zapLogger *zap.Logger
	stop      func() error
	// only used for init. will be nil at runtime.
	encoderFunc func(zapcore.EncoderConfig) zapcore.Encoder
	// enables async flush if positive. only used for init.
	asyncFlushDuration time.Duration
}

// Log - Gokit compatible Log function
func (z *ZapLogger) Log(keyvals ...interface{}) error {
	return z.kitLogger.Log(keyvals...)
}

// GetKitLogger - return gokit compatible logger
func (z *ZapLogger) GetKitLogger() log.Logger {
	return z.kitLogger
}

// GetZapLogger - return zap logger
func (z *ZapLogger) GetZapLogger() *zap.Logger {
	return z.zapLogger
}

// L - Zap compatible logger function
func (z *ZapLogger) L() *zap.Logger {
	return z.zapLogger
}

// S - Zap compatible sugared-logger function
func (z *ZapLogger) S() *zap.SugaredLogger {
	return z.zapLogger.Sugar()
}

// Close - closing any allocated buffer in zap logger
func (z *ZapLogger) Close() error {
	if z.stop != nil {
		return z.stop()
	}
	return nil
}

type LoggerOption func(z *ZapLogger)

func WithKitLogger(logger log.Logger) LoggerOption {
	return func(z *ZapLogger) {
		z.kitLogger = logger
	}
}
func WithZapLogger(logger *zap.Logger) LoggerOption {
	return func(z *ZapLogger) {
		z.zapLogger = logger
	}
}

func WithAsyncFlush(duration time.Duration) LoggerOption {
	return func(z *ZapLogger) {
		z.asyncFlushDuration = duration
	}
}

func WithEncoderFunc(f func(cfg zapcore.EncoderConfig) zapcore.Encoder) LoggerOption {
	return func(z *ZapLogger) {
		z.encoderFunc = f
	}
}

// NewZapLogger sets up a zap global logger and returns gokit compatible logger
func NewZapLogger(atom zap.AtomicLevel, loggerOpts ...LoggerOption) *ZapLogger {
	// Configure how logs should be encoded
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	encoderConfig.EncodeDuration = zapcore.MillisDurationEncoder
	// for encoderFunc and asyncFlushDuration
	logger := &ZapLogger{}
	for _, opt := range loggerOpts {
		opt(logger)
	}
	if logger.encoderFunc == nil {
		logger.encoderFunc = zapcore.NewConsoleEncoder
	}
	// Instantiate zap logger
	var bws *zapcore.BufferedWriteSyncer
	var ws zapcore.WriteSyncer
	if atom.Level() == zapcore.DebugLevel || logger.asyncFlushDuration <= 0 {
		ws = zapcore.Lock(os.Stdout)
	} else {
		bws = &zapcore.BufferedWriteSyncer{WS: os.Stdout, FlushInterval: logger.asyncFlushDuration}
		ws = bws
	}
	core := zapcore.NewCore(logger.encoderFunc(encoderConfig), ws, atom)
	opts := []zap.Option{
		zap.AddCaller(),
		zap.WithFatalHook(zapcore.WriteThenFatal),
	}
	zapLogger := zap.New(core, opts...)
	// This configures zap's global logger
	// This is needed so that the logger config can be instantiated once, and the same config is used across packages
	zap.ReplaceGlobals(zapLogger)

	// Return a logger compatible with gokit logger
	logger.kitLogger = kitzap.NewZapSugarLogger(zapLogger, atom.Level())
	logger.zapLogger = zapLogger
	logger.stop = func() error {
		if bws != nil {
			return bws.Stop()
		}
		return nil
	}
	for _, opt := range loggerOpts {
		opt(logger)
	}
	logger.encoderFunc = nil
	return logger
}

// NewAsyncZapLogger sets up a async zap global logger and returns gokit compatible logger
//
// Deprecated: Use NewZapLogger with WithAsyncFlush instead.
func NewAsyncZapLogger(atom zap.AtomicLevel, asyncFlushIntervalMs int, loggerOpts ...LoggerOption) *ZapLogger {
	loggerOpts = append(loggerOpts, WithAsyncFlush(time.Duration(asyncFlushIntervalMs)*time.Millisecond))
	return NewZapLogger(atom, loggerOpts...)
}

// GetZapFieldsFromContext - Gets any useful zap fields from context
// TODO: Explore if ctxzap is a better option
func GetZapFieldsFromContext(ctx context.Context) []zap.Field {
	var fields []zap.Field
	// Add Trace ID if available from context
	spanCtx := trace.SpanContextFromContext(ctx)
	traceID := spanCtx.TraceID().String()
	if len(traceID) != 0 {
		fields = append(fields, zap.String(KeyTraceID, spanCtx.TraceID().String()))
	}

	// Extract fields from custom handlers.
	// This assumes that the http handler has populated the necessary values in the request context
	if serverutils.CustomHandlerContext(ctx) {
		if reqID, ok := serverutils.RequestIDFromRequestContext(ctx); ok {
			fields = append(fields, zap.String(KeyRequestID, reqID))
		}
		return fields
	}

	// Extract fields from gRPC/gRPC-gateway handlers
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return fields
	}
	// Add Request ID if available from context
	reqID := md.Get(serverutils.HeaderKeyRequestID)
	if len(reqID) != 0 && len(reqID[0]) != 0 {
		fields = append(fields, zap.String(KeyRequestID, reqID[0]))
	}
	return fields
}

func GetZapFieldsForError(err error) []zap.Field {
	errStatus := status.Convert(err)
	for _, d := range errStatus.Details() {
		switch info := d.(type) { //nolint:gocritic
		case *nverrors.NVError:
			return []zap.Field{zap.String(KeyErrorID, info.ErrorId)}
		}
	}
	return nil
}
