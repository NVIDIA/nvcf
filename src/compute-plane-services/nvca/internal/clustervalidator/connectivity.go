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
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultConnectTimeout = 10 * time.Second

const (
	// inClusterCAPath is the standard mount path for the API server's CA bundle
	// inside any pod with automountServiceAccountToken: true.
	inClusterCAPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	// inClusterAPIURL is the in-cluster ClusterIP DNS name of the Kubernetes
	// API service. Probing this proves the service-routing layer (kube-proxy,
	// Cilium eBPF, OVN-Kubernetes, etc.) is working, regardless of which
	// implementation the cluster uses.
	inClusterAPIURL = "https://kubernetes.default.svc/readyz"
	// inClusterDNSName is the short name of the Kubernetes API service.
	// LookupHost succeeding on this name proves DNS works. Using the
	// short form (no `.cluster.local`) lets the kubelet-injected
	// resolv.conf search path resolve it correctly on clusters with a
	// non-default cluster domain (`--cluster-domain` override).
	inClusterDNSName = "kubernetes.default.svc"
)

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

// probeInClusterDNS resolves the Kubernetes API service's FQDN via the
// default OS resolver. Inside a Pod, /etc/resolv.conf is wired to the
// cluster's DNS service (CoreDNS, kube-dns, OpenShift DNS, etc.), so
// success proves DNS works regardless of which provider implements it.
// Brief retry loop covers init-container race conditions where the pod's
// network may not be fully wired in the first second.
func probeInClusterDNS(ctx context.Context) bool {
	// Backoff: 0.5s, 1s, 2s, 4s, 8s — total worst case ~22s before
	// declaring DNS broken (5 × 3s probe timeout + 0.5+1+2+4s sleeps
	// between attempts; the final 8s sleep is skipped). Fast enough
	// for an init container.
	backoffs := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
	}
	for attempt, backoff := range backoffs {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := net.DefaultResolver.LookupHost(probeCtx, inClusterDNSName)
		cancel()
		if err == nil && len(ips) > 0 {
			return true
		}
		// Don't sleep after the final attempt.
		if attempt < len(backoffs)-1 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return false
			}
		}
	}
	return false
}

// probeKubernetesAPIServiceIP issues GET kubernetes.default.svc/readyz —
// the API server reached via its in-cluster ClusterIP. Any HTTP response
// (200, 401, 403, even 5xx) proves the service-routing layer routed the
// ClusterIP to a backing pod and the TLS handshake completed. We don't
// care about the response status; only that the cluster's
// kube-proxy / eBPF / OVN-Kubernetes / etc. did its job.
//
// Uses the standard in-cluster CA bundle when present; falls back to
// InsecureSkipVerify only when the CA is unreadable (e.g., running outside
// a pod for dev/testing) — TLS verification of a remote API server is
// not the goal of this probe, the routing capability is.
func probeKubernetesAPIServiceIP(ctx context.Context) bool {
	client := &http.Client{
		Timeout: defaultConnectTimeout,
		Transport: &http.Transport{
			TLSClientConfig: inClusterTLSConfig(),
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inClusterAPIURL, nil)
	if err != nil {
		return false
	}
	// err is discarded intentionally: any non-nil response (even 4xx/5xx)
	// proves the ClusterIP routed to a TLS-speaking backend. A nil
	// response means DNS / TCP / TLS-handshake failure — caller treats
	// that as "routing did not work".
	resp, _ := client.Do(req)
	if resp != nil {
		resp.Body.Close()
		return true
	}
	return false
}

// inClusterTLSConfig returns a *tls.Config that trusts the cluster's CA
// when the standard SA-mount is present, or skips verification otherwise.
// Skipping verify is acceptable here because the probe is solely a
// "routing reaches a TLS-speaking endpoint" capability check.
func inClusterTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	if caBytes, err := os.ReadFile(inClusterCAPath); err == nil && pool.AppendCertsFromPEM(caBytes) {
		return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // #nosec G402 — routing-only probe
}
