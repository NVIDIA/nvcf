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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestClient(t *testing.T, handler http.HandlerFunc, host string) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{ServiceURL: srv.URL, APIKey: "nvapi-test", HostHeader: host})
	require.NoError(t, err)
	return c
}

func TestNewClientValidatesConfig(t *testing.T) {
	_, err := NewClient(Config{APIKey: "nvapi-test"})
	require.ErrorContains(t, err, "URL is not set")
	_, err = NewClient(Config{ServiceURL: "http://events.example.com"})
	require.ErrorContains(t, err, "API key is required")
}

func TestGetStats(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/ledger/namespace/fv-1/stats", r.URL.Path)
		assert.Equal(t, "Started,Pulling", r.URL.Query().Get("eventFilter"))
		assert.Equal(t, "Bearer nvapi-test", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"namespace":"fv-1","summary":{"total_contexts":1,"by_event":{"Started":1}},` +
			`"contexts":[{"context":"cluster_id=c,instance_id=i","event_name":"Started","timestamp":"t","first_seen":"f"}]}`))
	}, "")

	got, err := c.GetStats(context.Background(), "fv-1", StatsOptions{EventFilter: []string{"Started", "Pulling"}})
	require.NoError(t, err)
	assert.Equal(t, 1, got.Summary.TotalContexts)
	require.Len(t, got.Contexts, 1)
	assert.Equal(t, "Started", got.Contexts[0].EventName)
}

func TestListEventsSendsContextAndHost(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/ledger/namespace/fv-1/events", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "dep-1", q.Get("deployment_id"))
		assert.Equal(t, "gpu-a100", q.Get("gpu_specification_id"))
		assert.False(t, q.Has("instance_id"))
		assert.Equal(t, "events.example.com", r.Host)
		_, _ = w.Write([]byte(`{"namespace":"fv-1","context":"x","events":[{"event_name":"Pulled","timestamp":"t",` +
			`"details":{"severity":"INFO","body":"pulled","attributes":{"k8s.pod.name":"p"}}}]}`))
	}, "events.example.com")

	got, err := c.ListEvents(context.Background(), "fv-1", EventsContext{DeploymentID: "dep-1", GPUSpecification: "gpu-a100"}, EventsAttribute{})
	require.NoError(t, err)
	require.Len(t, got.Events, 1)
	assert.Equal(t, "p", got.Events[0].Details.Attributes["k8s.pod.name"])
}

// A k8s-object event's body is a map, not a string; decoding must not fail.
func TestListEventsDecodesStructuredBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"namespace":"fv-1","context":"x","events":[{"event_name":"Pod.utils.Started","timestamp":"t",` +
			`"details":{"severity":"INFO","body":{"type":"Normal","object":{"kind":"Pod","name":"p"}}}}]}`))
	}, "")

	got, err := c.ListEvents(context.Background(), "fv-1", EventsContext{}, EventsAttribute{})
	require.NoError(t, err)
	require.Len(t, got.Events, 1)
	body, ok := got.Events[0].Details.Body.(map[string]interface{})
	require.True(t, ok, "expected a structured body, got %T", got.Events[0].Details.Body)
	assert.Equal(t, "Normal", body["type"])
}

func TestListEventsSendsResourceAndAttributeFilter(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "res-1", q.Get("resource_id"))
		assert.Equal(t, "request-id", q.Get("attribute_key"))
		assert.Equal(t, "abc", q.Get("attribute_value"))
		_, _ = w.Write([]byte(`{"namespace":"fv-1","context":"resource_id=res-1","events":[]}`))
	}, "")

	_, err := c.ListEvents(context.Background(), "fv-1", EventsContext{ResourceID: "res-1"}, EventsAttribute{Key: "request-id", Value: "abc"})
	require.NoError(t, err)
}

func TestListEventsRequiresAttributeKeyAndValueTogether(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("no request expected")
	}, "")

	_, err := c.ListEvents(context.Background(), "fv-1", EventsContext{ResourceID: "res-1"}, EventsAttribute{Key: "request-id"})
	require.ErrorContains(t, err, "together")
	_, err = c.ListEvents(context.Background(), "fv-1", EventsContext{ResourceID: "res-1"}, EventsAttribute{Value: "abc"})
	require.ErrorContains(t, err, "together")
}

func TestGetReportsHTTPErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"title":"Forbidden"}`))
	}, "")
	_, err := c.GetStats(context.Background(), "fv-1", StatsOptions{})
	require.ErrorContains(t, err, "returned 403")
	require.ErrorContains(t, err, "Forbidden")
}
