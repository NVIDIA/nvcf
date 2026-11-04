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

package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// TestExtractEvent_AccountColumnsDisabled verifies that with account columns off,
// events behave as they did before the columns existed: the values stay in the
// attributes, they are not validated, and nothing is stored in the column fields.
func TestExtractEvent_AccountColumnsDisabled(t *testing.T) {
	off := withAccountColumns(false)

	t.Run("cloudevent keeps values in attributes and skips validation", func(t *testing.T) {
		ce := cloudevents.NewEvent()
		ce.SetID("test-id")
		ce.SetType("test.event")
		ce.SetSource("/test")
		ce.SetExtension("namespace", "tenant-1")
		ce.SetExtension("ncaid", "acct.with.dots")
		ce.SetExtension("functionid", "not-a-uuid")

		event, err := extractCloudEvent(context.Background(), &ce, off)
		require.NoError(t, err)
		assert.Empty(t, event.NcaID)
		assert.Empty(t, event.FunctionID)

		var details EventDetails
		require.NoError(t, json.Unmarshal(event.DetailsJSON, &details))
		assert.Equal(t, "acct.with.dots", details.Attributes["ncaid"])
		assert.Equal(t, "not-a-uuid", details.Attributes["functionid"])
	})

	t.Run("cloudevent functionVersionId is not required to be a UUID", func(t *testing.T) {
		ce := cloudevents.NewEvent()
		ce.SetID("test-id")
		ce.SetType("test.event")
		ce.SetSource("/test")
		ce.SetExtension("functionversionid", "version-1")

		event, err := extractCloudEvent(context.Background(), &ce, off)
		require.NoError(t, err)
		assert.Equal(t, "version-1", event.Namespace)
	})

	t.Run("otlp keeps values in attributes and does not alias the namespace", func(t *testing.T) {
		lr := createOTLPLogRecord("instance.creation", "tenant-1", "nvca", "", map[string]string{
			"nca_id":              "acct.with.dots",
			"function_id":         "not-a-uuid",
			"function_version_id": "other-version",
		})

		event, err := extractK8sEvent(context.Background(), lr, off)
		require.NoError(t, err)
		assert.Equal(t, "tenant-1", event.Namespace)
		assert.Empty(t, event.NcaID)
		assert.Empty(t, event.FunctionID)

		var details EventDetails
		require.NoError(t, json.Unmarshal(event.DetailsJSON, &details))
		assert.Equal(t, "acct.with.dots", details.Attributes["nca_id"])
		assert.Equal(t, "not-a-uuid", details.Attributes["function_id"])
		assert.Equal(t, "other-version", details.Attributes["function_version_id"])
	})
}

// TestProcessCloudEvents_AccountColumnsOffByDefault verifies a server built
// without the setting writes no account fields to the database.
func TestProcessCloudEvents_AccountColumnsOffByDefault(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newServerWithMock(t, mockDB, "pod.ready")
	ctx := makeStoreCtx(server)

	ce := makeCloudEvent(t, "event-1", "pod.ready", "pod-1", time.Now())
	ce.SetExtension("ncaid", "acct-1")
	ce.SetExtension("functionid", testFunctionID)

	result := server.processCloudEvents(ctx, []*cloudevents.Event{ce})
	require.Equal(t, 1, result.SuccessCount)

	require.Len(t, mockDB.storedBulkRecords, 1)
	assert.Empty(t, mockDB.storedBulkRecords[0].NcaID)
	assert.Empty(t, mockDB.storedBulkRecords[0].FunctionID)
	require.Len(t, mockDB.storedStatsEvents, 1)
	assert.Empty(t, mockDB.storedStatsEvents[0].NcaID)
}

// TestProcessCloudEvents_FilteredStatsCarryAccountFields verifies the filtered
// stats write receives the account fields.
func TestProcessCloudEvents_FilteredStatsCarryAccountFields(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newTestServerWithStatsConfig(t, mockDB, config.StatsConfig{
		FilteredStatsEnabledEventNames: []string{"pod.ready"},
	}).WithAccountColumns(true)
	ctx := makeStoreCtx(server)

	ce := makeCloudEvent(t, "event-1", "pod.ready", "pod-1", time.Now())
	ce.SetExtension("ncaid", "acct-1")
	ce.SetExtension("functionid", testFunctionID)

	result := server.processCloudEvents(ctx, []*cloudevents.Event{ce})
	require.Equal(t, 1, result.SuccessCount)

	require.Len(t, mockDB.upsertFilteredStatsCalls, 1)
	assert.Equal(t, "acct-1", mockDB.upsertFilteredStatsCalls[0].ncaID)
	assert.Equal(t, testFunctionID, mockDB.upsertFilteredStatsCalls[0].functionID)
}

const (
	testVersionID  = "0f230d58-275f-444e-b30e-9c363a076702"
	testFunctionID = "7c1f9a52-8a0e-4b6e-9d57-1a2b3c4d5e6f"
)

// TestExtractEvent_AccountFields verifies ncaId and functionId are parsed from
// both wire formats into dedicated fields, kept out of details, validated, and
// optional so producers that do not send them keep working.
func TestExtractEvent_AccountFields(t *testing.T) {
	tests := []struct {
		name       string
		nca        string
		function   string
		wantNca    string
		wantFn     string
		wantErrMsg string
	}{
		{name: "both set", nca: "nca_Abc-123", function: testFunctionID, wantNca: "nca_Abc-123", wantFn: testFunctionID},
		{name: "neither set"},
		{name: "nca only", nca: "acct-1", wantNca: "acct-1"},
		{name: "nca max length", nca: strings.Repeat("a", 128), wantNca: strings.Repeat("a", 128)},
		{name: "nca too long", nca: strings.Repeat("a", 129), wantErrMsg: "ncaId"},
		{name: "nca bad characters", nca: "acct.1", wantErrMsg: "ncaId"},
		{name: "function not a uuid", function: "my-function", wantErrMsg: "functionId"},
	}
	for _, tc := range tests {
		t.Run("cloudevent/"+tc.name, func(t *testing.T) {
			ce := cloudevents.NewEvent()
			ce.SetID("test-id")
			ce.SetType("test.event")
			ce.SetSource("/test")
			ce.SetExtension("namespace", testVersionID)
			if tc.nca != "" {
				ce.SetExtension("ncaid", tc.nca)
			}
			if tc.function != "" {
				ce.SetExtension("functionid", tc.function)
			}

			event, err := extractCloudEvent(context.Background(), &ce)
			if tc.wantErrMsg != "" {
				require.ErrorContains(t, err, tc.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantNca, event.NcaID)
			assert.Equal(t, tc.wantFn, event.FunctionID)

			var details EventDetails
			require.NoError(t, json.Unmarshal(event.DetailsJSON, &details))
			assert.NotContains(t, details.Attributes, "ncaid")
			assert.NotContains(t, details.Attributes, "functionid")
		})
		t.Run("otlp/"+tc.name, func(t *testing.T) {
			attrs := map[string]string{}
			if tc.nca != "" {
				attrs["nca_id"] = tc.nca
			}
			if tc.function != "" {
				attrs["function_id"] = tc.function
			}
			lr := createOTLPLogRecord("instance.creation", testVersionID, "nvca", "", attrs)

			event, err := extractK8sEvent(context.Background(), lr)
			if tc.wantErrMsg != "" {
				require.ErrorContains(t, err, tc.wantErrMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantNca, event.NcaID)
			assert.Equal(t, tc.wantFn, event.FunctionID)

			var details EventDetails
			require.NoError(t, json.Unmarshal(event.DetailsJSON, &details))
			assert.NotContains(t, details.Attributes, "nca_id")
			assert.NotContains(t, details.Attributes, "function_id")
		})
	}
}

// TestExtractK8sEvent_FunctionVersionIDNamespace mirrors the CloudEvents
// behavior: function_version_id may stand in for namespace.
func TestExtractK8sEvent_FunctionVersionIDNamespace(t *testing.T) {
	lr := createOTLPLogRecord("instance.creation", "", "nvca", "", map[string]string{"function_version_id": testVersionID})
	event, err := extractK8sEvent(context.Background(), lr)
	require.NoError(t, err)
	assert.Equal(t, testVersionID, event.Namespace)

	lr = createOTLPLogRecord("instance.creation", "other", "nvca", "", map[string]string{"function_version_id": testVersionID})
	_, err = extractK8sEvent(context.Background(), lr)
	require.ErrorContains(t, err, "must match")

	lr = createOTLPLogRecord("instance.creation", "", "nvca", "", map[string]string{"function_version_id": "not-a-uuid"})
	_, err = extractK8sEvent(context.Background(), lr)
	require.ErrorContains(t, err, "functionVersionId")
}

// TestProcessCloudEvents_PersistsAccountFields verifies the account fields reach
// the bulk upserts for both events_v3 and stats_v3, and that an event without
// them still goes through (today's producers).
func TestProcessCloudEvents_PersistsAccountFields(t *testing.T) {
	mockDB := &mockDBHandlerV3{}
	server := newServerWithMock(t, mockDB, "pod.ready").WithAccountColumns(true)
	ctx := makeStoreCtx(server)
	now := time.Now()

	withAccount := makeCloudEvent(t, "event-1", "pod.ready", "pod-1", now)
	withAccount.SetExtension("ncaid", "acct-1")
	withAccount.SetExtension("functionid", testFunctionID)
	legacy := makeCloudEvent(t, "event-2", "pod.ready", "pod-2", now)

	result := server.processCloudEvents(ctx, []*cloudevents.Event{withAccount, legacy})
	require.Equal(t, 2, result.SuccessCount)
	require.Equal(t, 0, result.FailureCount)

	for name, got := range map[string][]data_access.EventV3UpsertRecord{
		"events_v3": mockDB.storedBulkRecords,
		"stats_v3":  mockDB.storedStatsEvents,
	} {
		require.Len(t, got, 2, name)
		for _, r := range got {
			switch r.Context {
			case "instance_id=pod-1":
				assert.Equal(t, "acct-1", r.NcaID, name)
				assert.Equal(t, testFunctionID, r.FunctionID, name)
			case "instance_id=pod-2":
				assert.Empty(t, r.NcaID, name)
				assert.Empty(t, r.FunctionID, name)
			default:
				t.Errorf("%s: unexpected context %q", name, r.Context)
			}
		}
	}
}
