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

package health

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type livenessProbeHealthCheckerMock struct {
	success int32
}

func (m *livenessProbeHealthCheckerMock) StatusOK() bool {
	return atomic.LoadInt32(&m.success) > 0
}

func (m *livenessProbeHealthCheckerMock) Name() string { return "mock" }

func TestHTTPAddLivenessRoute(t *testing.T) {
	router := mux.NewRouter()
	mockVerifier1 := &livenessProbeHealthCheckerMock{}
	mockVerifier2 := &livenessProbeHealthCheckerMock{}

	// Ensure init works as expected.
	_, ok := NewLazyLivenessCheckGetter().GetCheckers()
	assert.False(t, ok)
	gotChecks, ok := NewLazyLivenessCheckGetter(mockVerifier1).GetCheckers()
	assert.True(t, ok)
	assert.Len(t, gotChecks, 1)

	// Ensure add/get work as expected.
	g := NewLazyLivenessCheckGetter()
	g.AddChecker(mockVerifier1)
	gotChecks, ok = g.GetCheckers()
	assert.Len(t, gotChecks, 1)
	assert.True(t, ok)
	g.AddChecker(mockVerifier2)
	gotChecks, ok = g.GetCheckers()
	assert.True(t, ok)
	assert.Len(t, gotChecks, 2)

	HTTPAddLivenessRoute(router, NewLazyLivenessCheckGetter(mockVerifier1, mockVerifier2))
	s := httptest.NewServer(router)
	t.Cleanup(s.Close)

	type test struct {
		success1                   int32
		success2                   int32
		expectedResponseStatusCode int
	}

	httpClient := &http.Client{}
	tests := map[string]test{
		"No verifier alive":                  {success1: -1, success2: -1, expectedResponseStatusCode: http.StatusServiceUnavailable},
		"One verifier live, other bad":       {success1: 1, success2: -1, expectedResponseStatusCode: http.StatusServiceUnavailable},
		"First verifier bad, second good":    {success1: -1, success2: 1, expectedResponseStatusCode: http.StatusServiceUnavailable},
		"verifier is not successful yet":     {success1: 0, success2: 0, expectedResponseStatusCode: http.StatusServiceUnavailable},
		"verifier is successful, one failed": {success1: 1, success2: -1, expectedResponseStatusCode: http.StatusServiceUnavailable},
		"verifier is successful":             {success1: 1, success2: 1, expectedResponseStatusCode: http.StatusOK},
	}
	for k, test := range tests {
		t.Run(k, func(t *testing.T) {
			atomic.StoreInt32(&mockVerifier1.success, test.success1)
			atomic.StoreInt32(&mockVerifier2.success, test.success2)
			req, err := http.NewRequest(http.MethodGet, s.URL+HTTPLivenessRoutePath, nil)
			require.NoError(t, err)
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, test.expectedResponseStatusCode, resp.StatusCode)
		})
	}
}
