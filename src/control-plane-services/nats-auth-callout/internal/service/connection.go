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
	"net"
	"os"
	"strings"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

const (
	eventDisconnect      = "disconnect"
	eventReconnect       = "reconnect"
	eventReconnectFailed = "reconnect_failed"
	eventClosed          = "closed"
	eventError           = "error"

	reasonNone    = "none"
	reasonTLS     = "tls"
	reasonAuth    = "auth"
	reasonTimeout = "timeout"
	reasonOther   = "other"
)

var (
	connectionEvents   = []string{eventDisconnect, eventReconnect, eventReconnectFailed, eventClosed, eventError}
	connectionReasons  = []string{reasonNone, reasonTLS, reasonAuth, reasonTimeout, reasonOther}
	connectionStatuses = []nats.Status{
		nats.DISCONNECTED, nats.CONNECTED, nats.CLOSED, nats.RECONNECTING,
		nats.CONNECTING, nats.DRAINING_SUBS, nats.DRAINING_PUBS,
	}
)

// Status reports the NATS connection status for health checks.
func (s *Service) Status() nats.Status {
	return s.nc.Status()
}

// connectionOptions keeps the connection retrying through any outage, so only
// a fatal error or Stop closes it, and reports every lifecycle event.
func (s *Service) connectionOptions() []nats.Option {
	return []nats.Option{
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			// nats.go also calls this handler on Close; the closed handler reports that case.
			if nc.IsClosed() {
				return
			}
			recordConnectionEvent(eventDisconnect, err, nc.Status())
			s.logger.Warn("NATS connection lost", zap.Error(err), zap.Stringer("status", nc.Status()))
		}),
		nats.ReconnectErrHandler(func(nc *nats.Conn, err error) {
			recordConnectionEvent(eventReconnectFailed, err, nc.Status())
			// Attempts repeat every few seconds during an outage; the disconnect warning already reports it.
			s.logger.Debug("NATS reconnect attempt failed", zap.Error(err))
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			recordConnectionEvent(eventReconnect, nil, nc.Status())
			s.logger.Info("NATS connection restored", zap.String("url", nc.ConnectedUrlRedacted()))
		}),
		nats.ClosedHandler(func(nc *nats.Conn) {
			err := nc.LastError()
			recordConnectionEvent(eventClosed, err, nc.Status())
			if s.stopping.Load() {
				s.logger.Info("NATS connection closed", zap.Error(err))
				return
			}
			s.logger.Error("NATS connection closed", zap.Error(err))
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			recordConnectionEvent(eventError, err, nc.Status())
			fields := []zap.Field{zap.Error(err)}
			if sub != nil {
				fields = append(fields, zap.String("subject", sub.Subject))
			}
			s.logger.Warn("NATS connection error", fields...)
		}),
	}
}

func recordConnectionEvent(event string, err error, status nats.Status) {
	NATSConnectionEventsTotal.WithLabelValues(event, connectionFailureReason(err)).Inc()
	setConnectionStatus(status)
}

func setConnectionStatus(current nats.Status) {
	for _, status := range connectionStatuses {
		value := 0.0
		if status == current {
			value = 1
		}
		NATSConnectionStatus.WithLabelValues(statusLabel(status)).Set(value)
	}
}

func statusLabel(status nats.Status) string {
	return strings.ToLower(status.String())
}

func connectionFailureReason(err error) string {
	if err == nil {
		return reasonNone
	}
	if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) ||
		errors.Is(err, nats.ErrAccountAuthExpired) {
		return reasonAuth
	}
	var (
		verificationErr *tls.CertificateVerificationError
		unknownAuthErr  x509.UnknownAuthorityError
		hostnameErr     x509.HostnameError
		invalidCertErr  x509.CertificateInvalidError
	)
	if errors.As(err, &verificationErr) || errors.As(err, &unknownAuthErr) ||
		errors.As(err, &hostnameErr) || errors.As(err, &invalidCertErr) ||
		errors.Is(err, nats.ErrSecureConnRequired) || errors.Is(err, nats.ErrSecureConnWanted) {
		return reasonTLS
	}
	var netErr net.Error
	if errors.Is(err, nats.ErrTimeout) || errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return reasonTimeout
	}
	return reasonOther
}
