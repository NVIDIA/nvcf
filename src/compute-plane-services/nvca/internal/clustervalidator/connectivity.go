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

package clustervalidator

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const defaultConnectTimeout = 10 * time.Second

// Reachability protocol identifiers as written in the user's ConfigMap.
const (
	protocolHTTPS  = "https"
	protocolTCP    = "tcp"
	protocolTCPTLS = "tcp+tls"
)

// Endpoint describes a service endpoint to check connectivity against.
type Endpoint struct {
	// URL is set for HTTPS endpoints.
	URL string
	// Host and Port are set for raw TCP / TCP+TLS endpoints.
	Host string
	Port int
	// Protocol is one of "https", "tcp", "tcp+tls".
	Protocol string
}

// DisplayAddr returns a human-readable address for log output.
func (e Endpoint) DisplayAddr() string {
	if e.URL != "" {
		return e.URL
	}
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// TestEndpoint checks connectivity to the endpoint based on its protocol.
func TestEndpoint(ep Endpoint) bool {
	switch ep.Protocol {
	case protocolHTTPS:
		return testHTTPS(ep.URL)
	case protocolTCP:
		return testTCP(ep.Host, ep.Port, false)
	case protocolTCPTLS:
		return testTCP(ep.Host, ep.Port, true)
	default:
		return false
	}
}

// testHTTPS performs an HTTP HEAD request. Any response (including HTTP errors,
// TLS handshake errors, or non-HTTP responses like gRPC) indicates the server
// is reachable.
func testHTTPS(url string) bool {
	client := &http.Client{
		Timeout: defaultConnectTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
		return true
	}

	if err != nil {
		return isTLSOrProtocolError(err)
	}

	return false
}

// isTLSOrProtocolError returns true if the error indicates the server was
// reached but the handshake or protocol negotiation failed. This includes
// client-certificate-required TLS errors and gRPC binary responses that
// cannot be parsed as HTTP.
func isTLSOrProtocolError(err error) bool {
	if _, ok := err.(*tls.CertificateVerificationError); ok {
		return true
	}
	// net/http wraps many errors; unwrap and check the inner error.
	if ue, ok := err.(*net.OpError); ok {
		if _, ok := ue.Err.(*tls.CertificateVerificationError); ok {
			return true
		}
	}
	// Any TLS record-layer or alert error means the server responded.
	errStr := err.Error()
	for _, substr := range []string{
		"tls:",
		"certificate required",
		"bad status line",
		"malformed HTTP",
	} {
		if strings.Contains(errStr, substr) {
			return true
		}
	}
	return false
}

// testTCP dials a TCP connection (optionally wrapping with TLS). An SSL
// handshake error still counts as reachable.
func testTCP(host string, port int, useTLS bool) bool {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, defaultConnectTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	if useTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.Handshake(); err != nil {
			// TLS error means the server responded, so it is reachable.
			return true
		}
		tlsConn.Close()
	}

	return true
}
