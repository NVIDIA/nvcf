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

package operator

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	nvcfv1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

func TestWebhookCertSetup(t *testing.T) {
	tmpFunc := getTLSDNSNames
	t.Cleanup(func() {
		getTLSDNSNames = tmpFunc
	})
	getTLSDNSNames = func(*nvcfv1.NVCFBackend) []string { return []string{"localhost"} }

	nb := &nvcfv1.NVCFBackend{}
	whCert, err := generateWebhookCerts(nb, time.Now())
	require.NoError(t, err)

	rootCAs := x509.NewCertPool()
	appendedCA := rootCAs.AppendCertsFromPEM(whCert.CACertBytes)
	require.True(t, appendedCA)

	serverCert, err := tls.X509KeyPair([]byte(whCert.TLSCert), []byte(whCert.TLSKey))
	require.NoError(t, err)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	trpt := http.DefaultTransport.(*http.Transport)
	trpt.TLSClientConfig = &tls.Config{
		RootCAs: rootCAs,
	}
	client := &http.Client{Transport: trpt}

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	nu := "https://localhost:" + u.Port()

	req, err := http.NewRequest("GET", nu, nil)
	require.NoError(t, err)
	res, err := client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
}
