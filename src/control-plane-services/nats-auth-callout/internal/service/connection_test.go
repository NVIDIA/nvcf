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

package service

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}

func TestConnectionFailureReason_ClassifiesErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, reasonNone},
		{"authorization violation", nats.ErrAuthorization, reasonAuth},
		{"authentication expired", nats.ErrAuthExpired, reasonAuth},
		{"account authentication expired", nats.ErrAccountAuthExpired, reasonAuth},
		{"wrapped authorization violation", fmt.Errorf("connect: %w", nats.ErrAuthorization), reasonAuth},
		{"unknown certificate authority", fmt.Errorf("handshake: %w", x509.UnknownAuthorityError{}), reasonTLS},
		{"certificate verification", &tls.CertificateVerificationError{Err: errors.New("bad cert")}, reasonTLS},
		{"hostname mismatch", x509.HostnameError{Host: "nats"}, reasonTLS},
		{"secure connection required", nats.ErrSecureConnRequired, reasonTLS},
		{"nats timeout", nats.ErrTimeout, reasonTimeout},
		{"deadline exceeded", os.ErrDeadlineExceeded, reasonTimeout},
		{"network timeout", &net.OpError{Op: "dial", Err: timeoutError{}}, reasonTimeout},
		{"connection reset", errors.New("read: connection reset by peer"), reasonOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectionFailureReason(tt.err); got != tt.want {
				t.Errorf("connectionFailureReason(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestConnectionMetrics_PreInitialized(t *testing.T) {
	if got, want := testutil.CollectAndCount(NATSConnectionEventsTotal), len(connectionEvents)*len(connectionReasons); got != want {
		t.Errorf("events series = %d, want %d (every event and reason pair)", got, want)
	}
	if got, want := testutil.CollectAndCount(NATSConnectionStatus), len(connectionStatuses); got != want {
		t.Errorf("status series = %d, want %d (every status)", got, want)
	}
}

func TestSetConnectionStatus_MarksOnlyCurrentStatus(t *testing.T) {
	for _, status := range []nats.Status{nats.CONNECTED, nats.RECONNECTING, nats.CLOSED} {
		t.Run(status.String(), func(t *testing.T) {
			setConnectionStatus(status)
			for _, s := range connectionStatuses {
				want := 0.0
				if s == status {
					want = 1
				}
				if got := testutil.ToFloat64(NATSConnectionStatus.WithLabelValues(statusLabel(s))); got != want {
					t.Errorf("status %s gauge = %v, want %v", statusLabel(s), got, want)
				}
			}
		})
	}
}

func TestConnectionOptions_HandlersLogAndCountEvents(t *testing.T) {
	tests := []struct {
		name       string
		stopping   bool
		fire       func(o *nats.Options, nc *nats.Conn)
		wantEvent  string
		wantReason string
		wantLog    string
		wantLevel  zapcore.Level
	}{
		{"disconnect", false, func(o *nats.Options, nc *nats.Conn) { o.DisconnectedErrCB(nc, io.EOF) },
			eventDisconnect, reasonOther, "NATS connection lost", zapcore.WarnLevel},
		{"reconnect attempt timed out", false, func(o *nats.Options, nc *nats.Conn) { o.ReconnectErrCB(nc, nats.ErrTimeout) },
			eventReconnectFailed, reasonTimeout, "NATS reconnect attempt failed", zapcore.DebugLevel},
		{"reconnect", false, func(o *nats.Options, nc *nats.Conn) { o.ReconnectedCB(nc) },
			eventReconnect, reasonNone, "NATS connection restored", zapcore.InfoLevel},
		{"async authorization error", false, func(o *nats.Options, nc *nats.Conn) { o.AsyncErrorCB(nc, nil, nats.ErrAuthorization) },
			eventError, reasonAuth, "NATS connection error", zapcore.WarnLevel},
		{"unexpected close", false, func(o *nats.Options, nc *nats.Conn) { o.ClosedCB(nc) },
			eventClosed, reasonNone, "NATS connection closed", zapcore.ErrorLevel},
		{"close on shutdown", true, func(o *nats.Options, nc *nats.Conn) { o.ClosedCB(nc) },
			eventClosed, reasonNone, "NATS connection closed", zapcore.InfoLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			s := &Service{logger: zap.New(core)}
			s.stopping.Store(tt.stopping)
			opts := &nats.Options{}
			for _, apply := range s.connectionOptions() {
				if err := apply(opts); err != nil {
					t.Fatalf("apply option: %v", err)
				}
			}
			counter := NATSConnectionEventsTotal.WithLabelValues(tt.wantEvent, tt.wantReason)
			before := testutil.ToFloat64(counter)

			tt.fire(opts, &nats.Conn{})

			if got := testutil.ToFloat64(counter) - before; got != 1 {
				t.Errorf("%s/%s events increased by %v, want 1", tt.wantEvent, tt.wantReason, got)
			}
			entries := logs.FilterMessage(tt.wantLog).All()
			if len(entries) != 1 || entries[0].Level != tt.wantLevel {
				t.Errorf("want one %q line at %s, got %v", tt.wantLog, tt.wantLevel, entries)
			}
		})
	}
}
