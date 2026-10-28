// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"log/slog"
	"regexp"
	"time"

	"github.com/go-logr/logr"
)

// plainMessage matches an SDK log message of a few lowercase words, such as
// "parse url". Other messages can carry a value and are withheld.
var plainMessage = regexp.MustCompile(`^[a-z]+( [a-z]+){0,7}$`)

// sdkSink receives the OpenTelemetry SDK's internal log. The SDK passes it
// raw values, such as an endpoint or a header it failed to parse, so it logs
// only a plain message and, for an error, a bounded reason. It drops info
// and debug, and logs each message at most once per interval.
type sdkSink struct{ limit *limiter }

func newSDKSink(interval time.Duration, now func() time.Time) *sdkSink {
	return &sdkSink{limit: newLimiter(interval, now)}
}

func (s *sdkSink) Init(logr.RuntimeInfo) {}

// Enabled keeps warnings (level 1) and drops info (4) and debug (8). logr
// passes errors through regardless.
func (s *sdkSink) Enabled(level int) bool { return level <= 1 }

func (s *sdkSink) Info(_ int, msg string, _ ...any) { s.log(msg) }

func (s *sdkSink) Error(err error, msg string, _ ...any) { s.log(msg, "reason", failureReason(err)) }

func (s *sdkSink) WithValues(...any) logr.LogSink { return s }

func (s *sdkSink) WithName(string) logr.LogSink { return s }

func (s *sdkSink) log(msg string, attrs ...any) {
	if !plainMessage.MatchString(msg) {
		msg = "withheld"
	}
	failures, ok := s.limit.admit(msg)
	if !ok {
		return
	}
	slog.Warn("telemetry SDK diagnostic, details withheld", append([]any{"message", msg, "failures", failures}, attrs...)...)
}
