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

// Package tlsreload serves a TLS certificate pair from files and picks up a
// renewed pair without a restart. A Loader loads the pair when it is created,
// then Run checks both files on an interval and swaps in the new pair when
// either changed. A pair that fails to load never replaces the last good one.
package tlsreload

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/telemetry"
)

// DefaultInterval is how often Run checks the files when no positive
// interval is configured.
const DefaultInterval = 30 * time.Second

// Option configures a Loader.
type Option func(*Loader)

// WithInterval sets how often Run checks the files. A non-positive interval
// keeps DefaultInterval.
func WithInterval(interval time.Duration) Option {
	return func(l *Loader) {
		if interval > 0 {
			l.interval = interval
		}
	}
}

// WithLogger sets the logger for loads and rejected reloads. The default is
// the service logger.
func WithLogger(logger zerolog.Logger) Option {
	return func(l *Loader) {
		l.logger = logger
	}
}

// Loader holds the certificate pair served over TLS. GetCertificate and Run
// are safe for concurrent use.
type Loader struct {
	certFile string
	keyFile  string
	interval time.Duration
	logger   zerolog.Logger

	current atomic.Pointer[tls.Certificate]

	// mu serializes reloads and guards the fields below.
	mu sync.Mutex
	// stamp identifies the files the current pair was loaded from. A failed
	// reload leaves it alone, so the next check retries until a pair loads.
	stamp pairStamp
	// lastErr is the last rejected reload error that was logged. It is reset
	// once the files match a served pair again, so a recurring error is
	// logged again.
	lastErr string
}

// fileStamp is the modification time and size of one file. Kubernetes
// updates a Secret volume by swapping a symlink to freshly written files, so
// a renewal changes the modification time of both.
type fileStamp struct {
	modTimeNanos int64
	size         int64
}

type pairStamp struct {
	cert fileStamp
	key  fileStamp
}

// New loads the pair from certFile and keyFile. It fails when the pair does
// not load, so a gateway never starts without a certificate.
func New(certFile, keyFile string, opts ...Option) (*Loader, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("tls certificate and key files are both required")
	}
	l := &Loader{
		certFile: certFile,
		keyFile:  keyFile,
		interval: DefaultInterval,
		logger:   *telemetry.Logger(context.Background()),
	}
	for _, opt := range opts {
		opt(l)
	}

	stamp, cert, err := l.load()
	if err != nil {
		return nil, fmt.Errorf("load tls certificate pair: %w", err)
	}
	l.install(context.Background(), stamp, cert)
	return l, nil
}

// GetCertificate returns the pair being served. It is meant for
// tls.Config.GetCertificate; each new handshake sees the latest good pair.
func (l *Loader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return l.current.Load(), nil
}

// Run checks the files every interval until ctx is done and reloads the pair
// when either file changed.
func (l *Loader) Run(ctx context.Context) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.reloadOnce(ctx)
		}
	}
}

type reloadOutcome int

const (
	reloadUnchanged reloadOutcome = iota
	reloadSucceeded
	reloadRejected
)

// reloadOnce reloads the pair if either file changed since the current pair
// was loaded. A pair that fails to load is rejected: the current pair stays,
// the error is logged once until it changes, and the next call retries.
func (l *Loader) reloadOnce(ctx context.Context) reloadOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()

	stamp, err := l.statPair()
	if err == nil && stamp == l.stamp {
		// The files are the served pair again, so a later failure is news.
		l.lastErr = ""
		return reloadUnchanged
	}
	var cert *tls.Certificate
	if err == nil {
		cert, err = l.loadPair()
	}
	if err != nil {
		telemetry.RecordTLSReload(ctx, telemetry.TLSReloadRejected)
		if msg := err.Error(); msg != l.lastErr {
			l.lastErr = msg
			l.logger.Warn().
				Err(err).
				Str("cert_file", l.certFile).
				Str("key_file", l.keyFile).
				Time("serving_not_after", l.current.Load().Leaf.NotAfter).
				Msg("rejected tls certificate reload, keeping the current certificate")
		}
		return reloadRejected
	}

	l.lastErr = ""
	l.install(ctx, stamp, cert)
	telemetry.RecordTLSReload(ctx, telemetry.TLSReloadSuccess)
	return reloadSucceeded
}

// load stats the files before reading them. A file replaced between the stat
// and the read then shows up as a change on the next check instead of being
// masked by a stamp taken after the read.
func (l *Loader) load() (pairStamp, *tls.Certificate, error) {
	stamp, err := l.statPair()
	if err != nil {
		return pairStamp{}, nil, err
	}
	cert, err := l.loadPair()
	if err != nil {
		return pairStamp{}, nil, err
	}
	return stamp, cert, nil
}

func (l *Loader) statPair() (pairStamp, error) {
	cert, err := statFile(l.certFile)
	if err != nil {
		return pairStamp{}, err
	}
	key, err := statFile(l.keyFile)
	if err != nil {
		return pairStamp{}, err
	}
	return pairStamp{cert: cert, key: key}, nil
}

func statFile(path string) (fileStamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{modTimeNanos: info.ModTime().UnixNano(), size: info.Size()}, nil
}

func (l *Loader) loadPair() (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(l.certFile, l.keyFile)
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("parse leaf certificate: %w", err)
		}
		cert.Leaf = leaf
	}
	return &cert, nil
}

// install serves cert from now on and records its expiry. The log names the
// leaf only; the private key is never logged.
func (l *Loader) install(ctx context.Context, stamp pairStamp, cert *tls.Certificate) {
	l.stamp = stamp
	l.current.Store(cert)
	telemetry.RecordTLSCertificateExpiry(ctx, cert.Leaf.NotAfter)
	l.logger.Info().
		Str("cert_file", l.certFile).
		Str("subject", cert.Leaf.Subject.String()).
		Strs("dns_names", cert.Leaf.DNSNames).
		Str("serial", cert.Leaf.SerialNumber.Text(16)).
		Time("not_after", cert.Leaf.NotAfter).
		Msg("loaded tls certificate")
}
