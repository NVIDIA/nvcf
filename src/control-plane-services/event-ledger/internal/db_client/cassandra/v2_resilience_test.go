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

package cassandra

import (
	"context"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// TestFailedCloudEventsLifecycle tests the complete lifecycle of failed CloudEvents
func TestFailedCloudEventsLifecycle(t *testing.T) {
	// Skip if no Cassandra is available
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}

	handler := &CassandraHandler{session: session}

	// Create context with logger
	logger := otelzap.New(zap.NewNop())
	ctx := logging.AttachLoggerToContext(context.Background(), logger)

	// Clean up any existing data before test
	_ = session.Query("TRUNCATE failed_cloudevents").Exec()

	// Test data
	eventType := "v1"
	eventData := []byte(`{"test": "data"}`)

	// 1. Store a failed event
	err := handler.StoreFailedCloudEvent(ctx, eventType, eventData)
	require.NoError(t, err, "Should store failed event")

	// 2. Stream events
	events := []data_access.FailedCloudEvent{}
	iterator := handler.StreamFailedCloudEvents(ctx, 10)
	for event := range iterator {
		events = append(events, event)
	}
	assert.Len(t, events, 1, "Should find one event")

	// Verify event data
	if len(events) > 0 {
		assert.Equal(t, eventType, events[0].EventType)
		assert.Equal(t, string(eventData), events[0].EventData)
		assert.Equal(t, 0, events[0].RetryCount)

		// 3. Update retry count
		err = handler.UpdateFailedCloudEventRetry(ctx, events[0].ID, 1)
		require.NoError(t, err, "Should update retry count")

		// 4. Verify update
		events2 := []data_access.FailedCloudEvent{}
		iterator2 := handler.StreamFailedCloudEvents(ctx, 10)
		for event := range iterator2 {
			events2 = append(events2, event)
		}
		assert.Len(t, events2, 1)
		if len(events2) > 0 {
			assert.Equal(t, 1, events2[0].RetryCount)
		}

		// 5. Delete the event
		err = handler.DeleteFailedCloudEvent(ctx, events[0].ID)
		require.NoError(t, err, "Should delete event")
	}

	// 6. Verify deletion
	count := 0
	iterator3 := handler.StreamFailedCloudEvents(ctx, 10)
	for _ = range iterator3 {
		count++
	}
	assert.Equal(t, 0, count, "Should find no events after deletion")
}

// TestLeaderElection tests the leader election mechanism
func TestLeaderElection(t *testing.T) {
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}

	handler1 := &CassandraHandler{session: session}
	handler2 := &CassandraHandler{session: session}

	// Create context with logger
	logger := otelzap.New(zap.NewNop())
	ctx := logging.AttachLoggerToContext(context.Background(), logger)

	instanceID1 := "instance-1"
	instanceID2 := "instance-2"
	leaseDuration := 2 * time.Second

	// 1. First instance becomes leader
	isLeader, err := handler1.TryBecomeLeader(ctx, instanceID1, leaseDuration)
	require.NoError(t, err)
	assert.True(t, isLeader, "First instance should become leader")

	// 2. Second instance cannot become leader immediately
	isLeader, err = handler2.TryBecomeLeader(ctx, instanceID2, leaseDuration)
	require.NoError(t, err)
	assert.False(t, isLeader, "Second instance should not become leader")

	// 3. First instance can renew
	isLeader, err = handler1.TryBecomeLeader(ctx, instanceID1, leaseDuration)
	require.NoError(t, err)
	assert.True(t, isLeader, "First instance should renew leadership")

	// 4. Wait for lease to expire
	time.Sleep(leaseDuration + 500*time.Millisecond)

	// 5. Second instance can now become leader
	isLeader, err = handler2.TryBecomeLeader(ctx, instanceID2, leaseDuration)
	require.NoError(t, err)
	assert.True(t, isLeader, "Second instance should become leader after lease expires")

	// Clean up
	_ = session.Query("DELETE FROM retry_leader WHERE service_name = ?", "cloudevents_retry").Exec()
}

// getTestSession returns a test Cassandra session if available
func getTestSession(t *testing.T) *gocql.Session {
	// Try to connect to local Cassandra (Docker Compose)
	cluster := gocql.NewCluster("127.0.0.1")
	cluster.Keyspace = "app"
	cluster.Consistency = gocql.One // Use One for local testing
	cluster.Timeout = 5 * time.Second
	cluster.ConnectTimeout = 5 * time.Second

	// Try with default Cassandra credentials
	cluster.Authenticator = gocql.PasswordAuthenticator{
		Username: "cassandra",
		Password: "cassandra",
	}

	session, err := cluster.CreateSession()
	if err != nil {
		t.Logf("Failed to connect to Cassandra: %v", err)
		return nil // Cassandra not available
	}

	// Clean up any existing test data
	_ = session.Query("TRUNCATE failed_cloudevents").Exec()
	_ = session.Query("DELETE FROM retry_leader WHERE service_name = ?", "cloudevents_retry").Exec()

	return session
}
