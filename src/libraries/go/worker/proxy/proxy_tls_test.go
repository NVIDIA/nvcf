/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package proxy

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/ca"
	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

// localCert is a self-signed certificate for 127.0.0.1, trusted by TestMain. Tests that
// need a TLS listener of their own use it; httptest servers use httptest's own
// certificate, which TestMain trusts alongside it.
var localCert tls.Certificate

// TestMain seeds the proxy trust pool before any test runs. ProxyCAs caches its pool on
// first use, so NVCF_PROXY_CA_FILE has to be set up front. Both httptest's fixed
// certificate and the locally generated one are trusted, so tests can use either.
func TestMain(m *testing.M) {
	certPEM, keyPEM := mustGenerateLocalCert()

	var err error
	localCert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(err)
	}

	probe := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	httptestPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: probe.Certificate().Raw})
	probe.Close()

	f, err := os.CreateTemp("", "nvcf-proxy-ca-*.pem")
	if err != nil {
		panic(err)
	}
	if _, err := f.Write(append(httptestPEM, certPEM...)); err != nil {
		panic(err)
	}
	if err := f.Close(); err != nil {
		panic(err)
	}
	if err := os.Setenv(ca.EnvCAFile, f.Name()); err != nil {
		panic(err)
	}

	code := m.Run()
	_ = os.Remove(f.Name())
	os.Exit(code)
}

func mustGenerateLocalCert() (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nvcf-proxy-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestDefaultProxyPort(t *testing.T) {
	require.Equal(t, "443", defaultProxyPort("https"))
	require.Equal(t, "80", defaultProxyPort("http"))
	require.Equal(t, "80", defaultProxyPort(""))
}

// TestDialProxy_PlaintextUnchanged guards the existing behaviour: a non-https scheme
// must still open a plain TCP connection.
func TestDialProxy_PlaintextUnchanged(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		c, aErr := ln.Accept()
		if aErr == nil {
			_ = c.Close()
		}
	}()

	u, err := url.Parse("http://" + ln.Addr().String() + "/v1/proxy")
	require.NoError(t, err)

	c, err := dialProxy(t.Context(), u, u.Host)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	require.IsType(t, &net.TCPConn{}, c)
}

func TestDialProxy_HTTPSNegotiatesTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	u, err := url.Parse(srv.URL + "/v1/proxy")
	require.NoError(t, err)

	c, err := dialProxy(t.Context(), u, u.Host)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	tlsConn, ok := c.(*tls.Conn)
	require.True(t, ok, "https scheme must produce a TLS connection")
	require.True(t, tlsConn.ConnectionState().HandshakeComplete)
}

// TestDialProxy_HTTPSAgainstPlaintextListener proves the https path really handshakes
// rather than falling through to a plain dial — this is the failure an operator sees
// today when the scheme says https but nothing terminates TLS.
func TestDialProxy_HTTPSAgainstPlaintextListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		c, aErr := ln.Accept()
		if aErr == nil {
			_ = c.Close()
		}
	}()

	u, err := url.Parse("https://" + ln.Addr().String() + "/v1/proxy")
	require.NoError(t, err)

	_, err = dialProxy(t.Context(), u, u.Host)
	require.Error(t, err)
}

// TestTcpConnect_OverTLS exercises the whole CONNECT exchange over TLS: request write,
// response parse, and the hijacked byte stream handed back to the caller.
func TestTcpConnect_OverTLS(t *testing.T) {
	setupLogger()

	const want = "pong"
	var gotAuth string

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		conn, _, hErr := w.(http.Hijacker).Hijack()
		if hErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, wErr := conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n" + want)); wErr != nil {
			return
		}
	}))
	defer srv.Close()

	c, err := tcpConnect(t.Context(), "req-id", &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig{
		ProxyURI:                srv.URL + "/v1/proxy",
		ProxyAuthorizationToken: "tok",
	})
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	require.Equal(t, "Bearer tok", gotAuth)

	got, err := bufio.NewReader(c).ReadString('g')
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// TestTcpConnect_TLSTerminatedInFrontOfPlaintextListener covers the deployment shape this
// change exists for: TLS terminates at a load balancer, which forwards plaintext to
// grpc-proxy's HTTP/1 CONNECT listener. The worker speaks TLS to the edge; the CONNECT
// server itself never sees it.
func TestTcpConnect_TLSTerminatedInFrontOfPlaintextListener(t *testing.T) {
	setupLogger()

	const want = "tunnelled"

	// Stand in for grpc-proxy's HTTP/1 CONNECT listener: plaintext, answers CONNECT, then
	// hands the raw stream back.
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()
	go func() {
		c, aErr := backend.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = c.Close() }()
		if _, rErr := http.ReadRequest(bufio.NewReader(c)); rErr != nil {
			return
		}
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\n\r\n" + want))
	}()

	// Stand in for the load balancer: terminates TLS, forwards bytes unchanged.
	edge, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{localCert},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	defer func() { _ = edge.Close() }()
	go func() {
		front, aErr := edge.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = front.Close() }()
		back, dErr := net.Dial("tcp", backend.Addr().String())
		if dErr != nil {
			return
		}
		defer func() { _ = back.Close() }()
		go func() { _, _ = io.Copy(back, front) }()
		_, _ = io.Copy(front, back)
	}()

	c, err := tcpConnect(t.Context(), "req-id", &pb.WorkerInvokeFunctionRequest_StatefulConfig_ConnectionConfig_HTTP1ConnectionConfig{
		ProxyURI:                "https://" + edge.Addr().String() + "/v1/proxy",
		ProxyAuthorizationToken: "tok",
	})
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	got := make([]byte, len(want))
	_, err = io.ReadFull(c, got)
	require.NoError(t, err)
	require.Equal(t, want, string(got))
}
