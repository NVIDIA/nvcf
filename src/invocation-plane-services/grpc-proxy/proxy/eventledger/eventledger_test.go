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
package eventledger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-grpc-proxy/proxy/metrics"
)

const testEventType = "com.nvidia.nvcf.test"

func testEvent(id string) cloudevents.Event {
	event := cloudevents.NewEvent()
	event.SetID(id)
	event.SetSource("grpc-proxy-test")
	event.SetType(testEventType)
	return event
}

func publishCount(result string) float64 {
	return testutil.ToFloat64(metrics.EventLedgerPublishCounter.WithLabelValues(testEventType, result))
}

func writeSecrets(t *testing.T, secrets string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.json")
	require.NoError(t, os.WriteFile(path, []byte(secrets), 0o600))
	return path
}

func newTestPublisher(t *testing.T, ledgerURL string) *Publisher {
	t.Helper()
	publisher, err := New(ledgerURL, writeSecrets(t, `{"eventLedgerToken":"test-token"}`), "")
	require.NoError(t, err)
	return publisher
}

func TestPublishSendsStructuredEventWithBearerToken(t *testing.T) {
	var gotAuth, gotContentType, gotPath string
	var got map[string]any
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotContentType, gotPath = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.Path
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ledger.Close()

	publisher := newTestPublisher(t, ledger.URL)
	sent := publishCount("sent")
	publisher.Publish(t.Context(), testEvent("event-1"))
	publisher.Close()

	assert.Equal(t, "Bearer test-token", gotAuth)
	assert.Equal(t, "application/cloudevents+json", gotContentType)
	assert.Equal(t, ingestPath, gotPath)
	assert.Equal(t, "event-1", got["id"])
	assert.Equal(t, sent+1, publishCount("sent"))
}

func TestPublishSendsEventWithOAuth2Token(t *testing.T) {
	var gotScope string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, r.ParseForm())
		gotScope = r.Form.Get("scope")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-token","token_type":"bearer","expires_in":3600}`))
	}))
	defer idp.Close()

	var gotAuth string
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ledger.Close()

	publisher, err := New(ledger.URL, writeSecrets(t, `{"id":"client","secret":"client-secret"}`), idp.URL)
	require.NoError(t, err)
	publisher.Publish(t.Context(), testEvent("event-1"))
	publisher.Close()

	assert.Equal(t, "Bearer oauth-token", gotAuth)
	assert.Equal(t, scope, gotScope)
}

func TestPublishRetriesUnavailableLedger(t *testing.T) {
	var attempts atomic.Int32
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ledger.Close()

	publisher := newTestPublisher(t, ledger.URL)
	sent := publishCount("sent")
	publisher.Publish(t.Context(), testEvent("event-1"))
	publisher.Close()

	assert.Equal(t, int32(2), attempts.Load())
	assert.Equal(t, sent+1, publishCount("sent"))
}

func TestPublishDoesNotRetryRejectedEvent(t *testing.T) {
	var attempts atomic.Int32
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ledger.Close()

	publisher := newTestPublisher(t, ledger.URL)
	failed := publishCount("failed")
	publisher.Publish(t.Context(), testEvent("event-1"))
	publisher.Close()

	assert.Equal(t, int32(1), attempts.Load())
	assert.Equal(t, failed+1, publishCount("failed"))
}

func TestPublishDropsWhenTooManySendsInFlight(t *testing.T) {
	release := make(chan struct{})
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ledger.Close()

	publisher := newTestPublisher(t, ledger.URL)
	dropped := publishCount("dropped")
	for range maxInflight + 1 {
		publisher.Publish(t.Context(), testEvent("event"))
	}
	assert.Equal(t, dropped+1, publishCount("dropped"))

	close(release)
	publisher.Close()
}

func TestNilPublisherDropsEvents(t *testing.T) {
	var publisher *Publisher
	publisher.Publish(t.Context(), testEvent("event-1"))
	publisher.Close()
}
