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

package cloudevents

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/common/core/types"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
)

func TestNewCloudEventsStorageClient_MissingEndpoint(t *testing.T) {
	cfg := config.CloudEventsConfig{
		Enabled: true,
	}

	client, err := NewCloudEventsStorageClient(cfg)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "endpoint is required")
}

func TestNewCloudEventsStorageClient_TokenEndpointMissingCredentials(t *testing.T) {
	cfg := config.CloudEventsConfig{
		Enabled:       true,
		Endpoint:      "https://example.com/events",
		TokenEndpoint: "https://example.com/token",
	}

	client, err := NewCloudEventsStorageClient(cfg)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "credentials are required")
}

func TestNewCloudEventsStorageClient_CredsFileWithoutRefreshInterval(t *testing.T) {
	cfg := config.CloudEventsConfig{
		Enabled:                    true,
		Endpoint:                   "https://example.com/events",
		TokenEndpoint:              "https://example.com/token",
		CredentialsFile:            "/path/to/creds.yaml",
		CredentialsRefreshInterval: 0, // Invalid - must be > 0 when creds_file is specified
	}

	client, err := NewCloudEventsStorageClient(cfg)

	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "creds_refresh_interval must be greater than 0")
}

func TestCloudEventsStorageClient_StoreBatchPostsCloudEventsBatch(t *testing.T) {
	var receivedContentType string
	var receivedEvents []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		require.Equal(t, http.MethodPost, r.Method)

		err := json.NewDecoder(r.Body).Decode(&receivedEvents)
		require.NoError(t, err)

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewCloudEventsStorageClient(config.CloudEventsConfig{
		Enabled:  true,
		Endpoint: server.URL,
	})
	require.NoError(t, err)

	eventTime := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	err = client.StoreBatch(t.Context(), []types.StageTransitionEvent{
		{
			NcaId:             "nca-1",
			FunctionId:        uuid.New(),
			FunctionVersionId: uuid.New(),
			InstanceId:        "instance-1",
			Event:             "ready",
			EventType:         "sis",
			Timestamp:         eventTime,
			Details:           json.RawMessage(`{"ok":true}`),
		},
	})

	require.NoError(t, err)
	assert.Equal(t, cloudEventsBatchContentType, receivedContentType)
	require.Len(t, receivedEvents, 1)
	assert.Equal(t, "1.0", receivedEvents[0]["specversion"])
	assert.Equal(t, "event-ledger", receivedEvents[0]["source"])
	assert.NotEmpty(t, receivedEvents[0]["id"])
	assert.NotEmpty(t, receivedEvents[0]["data"])
}
