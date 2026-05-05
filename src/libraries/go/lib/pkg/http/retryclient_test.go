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

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
)

func TestRoundTrip(t *testing.T) {
	version.Version = "1.0.0"
	version.GitHash = ""
	version.Dirty = ""
	version.ReleaseTag = ""
	t.Cleanup(func() {
		version.Version = ""
	})
	httpClient := NewRetryableClient(context.Background(), WithAppVersionUserAgent("testApp"))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "testApp/1.0.0", r.Header.Get("User-Agent"))
	}))
	t.Cleanup(s.Close)

	resp, err := httpClient.Get(s.URL + "/versiontest")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Test with non-default client timeout
	timeout := 10 * time.Millisecond
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(timeout + time.Millisecond)
	}))
	t.Cleanup(s.Close)
	httpClient = NewRetryableClient(context.Background(), WithAppVersionUserAgent("testApp"), WithClientTimeout(timeout))
	resp, err = httpClient.Get(s.URL + "/versiontest")
	require.Error(t, err)
	assert.Nil(t, resp)

	// Test with non-default client timeout set via env var
	os.Setenv(ClientTimeoutEnvKey, timeout.String())
	t.Cleanup(func() { os.Unsetenv(ClientTimeoutEnvKey) })
	httpClient = NewRetryableClient(context.Background(), WithAppVersionUserAgent("testApp"))
	resp, err = httpClient.Get(s.URL + "/versiontest")
	require.Error(t, err)
	assert.Nil(t, resp)

	// Ensure a bad parse falls back to default
	os.Setenv(ClientTimeoutEnvKey, "some-bad-value")
	t.Cleanup(func() { os.Unsetenv(ClientTimeoutEnvKey) })
	httpClient = NewRetryableClient(context.Background(), WithAppVersionUserAgent("testApp"))
	resp, err = httpClient.Get(s.URL + "/versiontest")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Ensure empty falls back to default
	os.Setenv(ClientTimeoutEnvKey, "")
	t.Cleanup(func() { os.Unsetenv(ClientTimeoutEnvKey) })
	httpClient = NewRetryableClient(context.Background(), WithAppVersionUserAgent("testApp"))
	resp, err = httpClient.Get(s.URL + "/versiontest")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRoundTripRetryError(t *testing.T) {
	httpClient := NewRetryableClient(context.Background())
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("User-Agent"), "Go-http-client")
		http.Error(w, "server error, force retry", http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)

	resp, err := httpClient.Get(s.URL)
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.ErrorContains(t, err, "giving up")
}

func TestRetryableClientWithRequestHeader(t *testing.T) {
	tests := []struct {
		name        string
		reqHeaders  map[string]string
		headers     map[string]string
		expectedHdr map[string][]string
	}{
		{
			name:       "add header",
			reqHeaders: map[string]string{},
			headers: map[string]string{
				"Foo": "Bar",
			},
			expectedHdr: map[string][]string{
				"Foo": {"Bar"},
			},
		},
		{
			name: "overwrite header",
			reqHeaders: map[string]string{
				"Foo": "ClientValue",
			},
			headers: map[string]string{
				"Foo": "Bar",
			},
			expectedHdr: map[string][]string{
				"Foo": {"Bar"},
			},
		},
		{
			name:       "multiple headers",
			reqHeaders: map[string]string{},
			headers: map[string]string{
				"Foo": "Bar",
				"Baz": "Qux",
			},
			expectedHdr: map[string][]string{
				"Foo": {"Bar"},
				"Baz": {"Qux"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Subset(t, r.Header, tt.expectedHdr)
			}))
			t.Cleanup(s.Close)
			c := &http.Client{
				Timeout: time.Second,
			}
			c.Transport = newClientRequestHeadersTransport(nil, tt.headers)
			req, err := http.NewRequest(http.MethodGet, s.URL, nil)
			for k, v := range tt.reqHeaders {
				req.Header.Set(k, v)
			}
			require.NoError(t, err)
			resp, err := c.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() {
				if resp != nil {
					resp.Body.Close()
				}
			})
		})
	}
}

func TestRetryableClientWithClientOptions(t *testing.T) {
	c1 := newRetryableClient(context.Background(), &features{
		co: ClientOptions{
			RetryWaitMin: 1,
			RetryWaitMax: 2,
			RetryMax:     3,
			CheckRetry:   retryablehttp.ErrorPropagatedRetryPolicy,
			Backoff:      retryablehttp.LinearJitterBackoff,
			ErrorHandler: retryablehttp.PassthroughErrorHandler,
			PrepareRetry: func(req *http.Request) error { return nil },
		},
	})

	assert.EqualValues(t, 1, c1.RetryWaitMin)
	assert.EqualValues(t, 2, c1.RetryWaitMax)
	assert.EqualValues(t, 3, c1.RetryMax)
	assert.NotNil(t, c1.CheckRetry)
	assert.NotNil(t, c1.Backoff)
	assert.NotNil(t, c1.ErrorHandler)
	assert.NotNil(t, c1.PrepareRetry)

	c2 := newRetryableClient(context.Background(), &features{})

	assert.EqualValues(t, 1*time.Second, c2.RetryWaitMin)
	assert.EqualValues(t, 30*time.Second, c2.RetryWaitMax)
	assert.EqualValues(t, 4, c2.RetryMax)
	assert.NotNil(t, c2.CheckRetry)
	assert.NotNil(t, c2.Backoff)
	assert.Nil(t, c2.ErrorHandler)
	assert.Nil(t, c2.PrepareRetry)
}

func TestNewRetryableClient_InvalidTimeout(t *testing.T) {
	// Inject a spy logger so we can assert that a warning is emitted.
	ctx, hook := core.WithTestingLogger(context.Background())

	// Set invalid timeout env var
	os.Setenv(ClientTimeoutEnvKey, "invalid")
	t.Cleanup(func() { os.Unsetenv(ClientTimeoutEnvKey) })

	// Should use default timeout and log a warning about the parse failure.
	client := NewRetryableClient(ctx)
	assert.NotNil(t, client)
	assert.Equal(t, 5*time.Minute, client.Timeout)

	// Verify that the failed-parse warning was actually logged.
	var warned bool
	for _, e := range hook.AllEntries() {
		if e.Level == logrus.WarnLevel {
			warned = true
			break
		}
	}
	assert.True(t, warned, "expected a Warn-level log entry when timeout env var cannot be parsed")
}
