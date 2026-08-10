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

package reachability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckRequiresHostHeaderForGatewayAddress(t *testing.T) {
	err := Check(context.Background(), CheckRequest{
		TargetClusterName: "gpu-a",
		ICMSURL:           "http://127.0.0.1:8080",
		ReValURL:          "http://127.0.0.1:8080",
		NATSURL:           "nats://nats.example.test:4222",
		ProbeHTTP:         false,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "gpu-a")
	assert.Contains(t, err.Error(), "controlPlane.hosts.sis")
	assert.Contains(t, err.Error(), "controlPlane.hosts.reval")
}

func TestCheckErrorIncludesClusterNameWithoutProblems(t *testing.T) {
	err := (&CheckError{TargetClusterName: "gpu-a"}).Error()

	assert.Contains(t, err, "gpu-a")
	assert.NotContains(t, err, "\n- ")
}

func TestCheckProbesICMSAndReValWithHostHeaders(t *testing.T) {
	var gotHosts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHosts = append(gotHosts, r.Host)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	err := Check(context.Background(), CheckRequest{
		TargetClusterName: "gpu-a",
		ICMSURL:           server.URL,
		ReValURL:          server.URL,
		NATSURL:           "tls://nats.example.test:4222",
		SISHost:           "sis.example.test",
		ReValHost:         "reval.example.test",
		HTTPClient:        server.Client(),
		ProbeHTTP:         true,
	})

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"sis.example.test", "reval.example.test"}, gotHosts)
}

func TestCheckReportsHTTPProbeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	err := Check(context.Background(), CheckRequest{
		TargetClusterName: "gpu-a",
		ICMSURL:           server.URL,
		ReValURL:          server.URL,
		NATSURL:           "tls://nats.example.test:4222",
		SISHost:           "sis.example.test",
		ReValHost:         "reval.example.test",
		HTTPClient:        server.Client(),
		ProbeHTTP:         true,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "controlPlane.endpoints.computeReachable.icmsURL")
	assert.Contains(t, err.Error(), "status 502")
}

func TestCheckRejectsInvalidNATSURL(t *testing.T) {
	err := Check(context.Background(), CheckRequest{
		TargetClusterName: "gpu-a",
		ICMSURL:           "https://sis.example.test",
		ReValURL:          "https://reval.example.test",
		NATSURL:           "http://nats.example.test:4222",
		ProbeHTTP:         false,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "controlPlane.endpoints.computeReachable.natsURL")
	assert.True(t, strings.Contains(err.Error(), "nats") || strings.Contains(err.Error(), "tls"))
	assert.Contains(t, err.Error(), "TCP reachability is not probed")
}
