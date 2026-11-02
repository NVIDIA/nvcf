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

package httpstream

import (
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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/ca"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

var (
	trustedCA   *testCA
	untrustedCA *testCA
)

// ca.ProxyCAs builds its pool once per process, so the trusted CA must be in
// NVCF_PROXY_CA_PEM before any test creates a client.
func TestMain(m *testing.M) {
	trustedCA = newTestCA("worker-utils trusted test CA")
	untrustedCA = newTestCA("worker-utils untrusted test CA")
	_ = os.Setenv(ca.EnvCAPEM, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: trustedCA.cert.Raw})))
	os.Exit(m.Run())
}

func newTestCA(name string) *testCA {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return &testCA{cert: cert, key: key}
}

// serverCert issues a 127.0.0.1 server certificate signed by c.
func (c *testCA) serverCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newTLSServer(t *testing.T, issuer *testCA, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{issuer.serverCert(t)}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

// connectProxy is an https proxy that only tunnels CONNECT, like Envoy on the
// worker callback path. It records each tunnelled authority.
type connectProxy struct {
	mu      sync.Mutex
	targets []string
}

func (p *connectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
		return
	}
	p.mu.Lock()
	p.targets = append(p.targets, r.Host)
	p.mu.Unlock()
	upstream, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	downstream, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	go func() {
		_, _ = io.Copy(upstream, buf)
		_ = upstream.Close()
	}()
	_, _ = io.Copy(downstream, upstream)
	_ = downstream.Close()
}

func (p *connectProxy) tunnelled() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

func attachMux(body string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("POST /v2/nvcf/worker/request-attach", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	})
	return mux
}

func tlsStatelessConfig(target, proxy string) *pb.WorkerInvokeFunctionRequest_StatelessConfig {
	return &pb.WorkerInvokeFunctionRequest_StatelessConfig{
		ConnectionConfigs: []*pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig{{
			Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_Http1Config{
				Http1Config: &pb.WorkerInvokeFunctionRequest_StatelessConfig_ConnectionConfig_HTTP1ProtocolConfig{
					TargetURI:                  target,
					ProxyURI:                   lo.ToPtr(proxy),
					ResponseAuthorizationToken: "response-token",
					RequestAuthorizationToken:  lo.ToPtr("request-token"),
				},
			},
		}},
	}
}

func readClientBody(t *testing.T, target, proxy string) (string, error) {
	t.Helper()
	handler, err := NewRequestStreamHandler(t.Context(), NewProxiedClient(), tlsStatelessConfig(target, proxy),
		&pb.WorkerInvokeFunctionRequest{RequestId: "tls-test", RequestMethod: http.MethodPost, RequestPath: "/"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = handler.Close() })
	body, err := handler.GetClientRequestBody()
	if err != nil {
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	err = handler.SendResponse(t.Context(), &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"result":"ok"}`)),
	}, nil)
	return string(data), err
}

// Worker -> TLS to the proxy -> CONNECT -> TLS to invocation-service, with
// both certificates from the CA in NVCF_PROXY_CA_PEM.
func TestProxiedClient_TLSProxyAndTLSTarget(t *testing.T) {
	target := newTLSServer(t, trustedCA, attachMux(`{"test":"data"}`))
	proxy := &connectProxy{}
	proxyServer := newTLSServer(t, trustedCA, proxy)

	body, err := readClientBody(t, target.URL, proxyServer.URL)
	require.NoError(t, err)
	assert.Equal(t, `{"test":"data"}`, body)

	targetHost := strings.TrimPrefix(target.URL, "https://")
	tunnelled := proxy.tunnelled()
	require.NotEmpty(t, tunnelled)
	for _, host := range tunnelled {
		assert.Equal(t, targetHost, host, "every request must be tunnelled to the target")
	}
}

func TestProxiedClient_RejectsUntrustedTarget(t *testing.T) {
	target := newTLSServer(t, untrustedCA, attachMux("unreachable"))
	proxyServer := newTLSServer(t, trustedCA, &connectProxy{})

	_, err := readClientBody(t, target.URL, proxyServer.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify certificate")
	assert.NotContains(t, err.Error(), "proxyconnect", "the proxy is trusted; the target must be the one rejected")
}

func TestProxiedClient_RejectsUntrustedProxy(t *testing.T) {
	target := newTLSServer(t, trustedCA, attachMux("unreachable"))
	proxy := &connectProxy{}
	proxyServer := newTLSServer(t, untrustedCA, proxy)

	_, err := readClientBody(t, target.URL, proxyServer.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "proxyconnect")
	assert.Contains(t, err.Error(), "failed to verify certificate")
	assert.Empty(t, proxy.tunnelled(), "no tunnel may open through an untrusted proxy")
}
