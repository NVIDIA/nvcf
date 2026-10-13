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

package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/client"
)

func setupEventLedgerCommandTest(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	t.Setenv("NVCF_API_KEY", "")
	viper.Reset()
	viper.Set("api_key", "nvapi-test")
	viper.Set("event_ledger_url", server.URL)
	t.Cleanup(viper.Reset)

	jsonOutput = false
	eventLedgerURLFlag = ""
	eventLedgerFunctionVersionFlag = "fv-1"
	statsEventFilter = nil
	eventsInstanceID, eventsDeploymentID, eventsGPUSpecificationID, eventsClusterID = "", "", "", ""
	eventsResourceID, eventsAttributeKey, eventsAttributeValue = "", "", ""
	t.Cleanup(func() {
		jsonOutput = false
		eventLedgerFunctionVersionFlag = ""
		statsEventFilter = nil
		eventsInstanceID, eventsDeploymentID, eventsGPUSpecificationID, eventsClusterID = "", "", "", ""
		eventsResourceID, eventsAttributeKey, eventsAttributeValue = "", "", ""
	})
}

func TestStatsGetRendersContexts(t *testing.T) {
	setupEventLedgerCommandTest(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/ledger/namespace/fv-1/stats", r.URL.Path)
		assert.Equal(t, "Bearer nvapi-test", r.Header.Get("Authorization"))
		assert.Equal(t, "Started", r.URL.Query().Get("eventFilter"))
		_, _ = w.Write([]byte(`{"namespace":"fv-1","summary":{"total_contexts":1,"by_event":{"Started":1}},` +
			`"contexts":[{"context":"cluster_id=c1,instance_id=i1","event_name":"Started","timestamp":"2026-01-01T00:00:00Z","first_seen":"2026-01-01T00:00:00Z"}]}`))
	})
	statsEventFilter = []string{"Started"}

	var out bytes.Buffer
	statsGetCmd.SetOut(&out)
	t.Cleanup(func() { statsGetCmd.SetOut(nil) })
	require.NoError(t, statsGetCmd.RunE(statsGetCmd, nil))
	assert.Contains(t, out.String(), "Contexts: 1")
	assert.Contains(t, out.String(), "cluster_id=c1,instance_id=i1")
}

func TestStagesEventsListRequiresContextFilter(t *testing.T) {
	setupEventLedgerCommandTest(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request expected without a context filter")
	})
	err := stagesEventsListCmd.RunE(stagesEventsListCmd, nil)
	require.ErrorContains(t, err, "at least one of")
}

func TestStagesEventsListRendersEvents(t *testing.T) {
	setupEventLedgerCommandTest(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v3/ledger/namespace/fv-1/events", r.URL.Path)
		assert.Equal(t, "dep-1", r.URL.Query().Get("deployment_id"))
		assert.Equal(t, "gpu-a100", r.URL.Query().Get("gpu_specification_id"))
		_, _ = w.Write([]byte(`{"namespace":"fv-1","context":"deployment_id=dep-1","events":[` +
			`{"event_name":"Pulled","timestamp":"2026-01-01T00:00:01Z","details":{"severity":"INFO","body":"Successfully pulled image"}}]}`))
	})
	eventsDeploymentID = "dep-1"
	eventsGPUSpecificationID = "gpu-a100"

	var out bytes.Buffer
	stagesEventsListCmd.SetOut(&out)
	t.Cleanup(func() { stagesEventsListCmd.SetOut(nil) })
	require.NoError(t, stagesEventsListCmd.RunE(stagesEventsListCmd, nil))
	assert.Contains(t, out.String(), "Pulled")
	assert.Contains(t, out.String(), "Successfully pulled image")
}

func TestResolveEventLedgerURL(t *testing.T) {
	t.Cleanup(viper.Reset)
	viper.Reset()

	assert.Equal(t, "https://flag.example.com", resolveEventLedgerURL("https://flag.example.com", "https://api.example.com"))
	assert.Equal(t, "https://events.example.com", resolveEventLedgerURL("", "https://api.example.com"))
	assert.Empty(t, resolveEventLedgerURL("", "https://gateway.example.com"), "no events host can be derived without an api. prefix")

	viper.Set("event_ledger_url", "https://configured.example.com")
	assert.Equal(t, "https://configured.example.com", resolveEventLedgerURL("", "https://api.example.com"))
}

func TestEventLedgerAPIKeyPrefersDedicatedKey(t *testing.T) {
	assert.Equal(t, "nvapi-el", eventLedgerAPIKey(&client.Config{APIKey: "nvapi-fn", EventLedgerAPIKey: "nvapi-el"}))
	assert.Equal(t, "nvapi-fn", eventLedgerAPIKey(&client.Config{APIKey: "nvapi-fn"}))
}

func TestStagesEventsListResourceIDCountsAsContextAndSendsAttributeFilter(t *testing.T) {
	setupEventLedgerCommandTest(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.Equal(t, "res-1", q.Get("resource_id"))
		assert.Equal(t, "request-id", q.Get("attribute_key"))
		assert.Equal(t, "abc", q.Get("attribute_value"))
		_, _ = w.Write([]byte(`{"namespace":"fv-1","context":"resource_id=res-1","events":[]}`))
	})
	eventsResourceID = "res-1"
	eventsAttributeKey = "request-id"
	eventsAttributeValue = "abc"

	require.NoError(t, stagesEventsListCmd.RunE(stagesEventsListCmd, nil))
}

func TestStagesEventsListAttributeFilterNeedsKeyAndValue(t *testing.T) {
	setupEventLedgerCommandTest(t, func(http.ResponseWriter, *http.Request) {
		t.Error("no request expected with a half-specified attribute filter")
	})
	eventsDeploymentID = "dep-1"
	eventsAttributeKey = "request-id"

	err := stagesEventsListCmd.RunE(stagesEventsListCmd, nil)
	require.ErrorContains(t, err, "must be used together")
}
