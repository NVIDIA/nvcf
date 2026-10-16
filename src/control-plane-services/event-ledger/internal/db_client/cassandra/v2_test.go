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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

func TestFilteredStatsTableNameCompatibility(t *testing.T) {
	assert.Equal(t, "stats_v3_ngc", filteredStatsV3Table)
}

func TestUpsertFilteredStatsV3KeepsLatestEvent(t *testing.T) {
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}

	handler := &CassandraHandler{session: session}
	logger := otelzap.New(zap.NewNop())
	ctx := logging.AttachLoggerToContext(context.Background(), logger)

	_ = session.Query("TRUNCATE " + filteredStatsV3Table).Exec()
	t.Cleanup(func() { _ = session.Query("TRUNCATE " + filteredStatsV3Table).Exec() })

	createdAt := time.Now().Add(-2 * time.Minute).Truncate(time.Millisecond)
	latestTimestamp := createdAt.Add(2 * time.Minute)
	staleTimestamp := createdAt.Add(time.Minute)

	require.NoError(t, handler.UpsertFilteredStatsV3(ctx, "ns-latest", "ctx-1", "pod.pending", createdAt))
	require.NoError(t, handler.UpsertFilteredStatsV3(ctx, "ns-latest", "ctx-1", "pod.ready", latestTimestamp))
	require.NoError(t, handler.UpsertFilteredStatsV3(ctx, "ns-latest", "ctx-1", "pod.starting", staleTimestamp))

	var eventName string
	var storedTimestamp time.Time
	var storedCreatedAt time.Time
	err := session.Query(
		`SELECT event_name, timestamp, created_at FROM stats_v3_ngc WHERE namespace = ? AND context = ?`,
		"ns-latest",
		"ctx-1",
	).Scan(&eventName, &storedTimestamp, &storedCreatedAt)
	require.NoError(t, err)
	assert.Equal(t, "pod.ready", eventName)
	assert.Equal(t, latestTimestamp.UTC(), storedTimestamp.UTC().Truncate(time.Millisecond))
	assert.Equal(t, createdAt.UTC(), storedCreatedAt.UTC().Truncate(time.Millisecond))
}

func TestUpsertEventV3PreservesCreatedAt(t *testing.T) {
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}

	handler := &CassandraHandler{session: session}
	ctx := logging.AttachLoggerToContext(context.Background(), otelzap.New(zap.NewNop()))
	first := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	second := first.Add(time.Minute)
	const namespace, eventContext, name = "single-event-upsert", "ctx-1", "pod.ready"
	t.Cleanup(func() {
		_ = session.Query(`DELETE FROM events_v3 WHERE namespace = ? AND context = ? AND event_name = ?`, namespace, eventContext, name).Exec()
	})

	require.NoError(t, handler.UpsertEventV3(ctx, namespace, eventContext, name, "first", []byte(`{"value":1}`), first))
	var initialUpdatedAt time.Time
	require.NoError(t, session.Query(`SELECT updated_at FROM events_v3 WHERE namespace = ? AND context = ? AND event_name = ?`,
		namespace, eventContext, name).Scan(&initialUpdatedAt))
	// The single-event method now shares the bulk writer's ingestion-time metadata.
	assert.WithinDuration(t, time.Now(), initialUpdatedAt, time.Minute)
	assert.True(t, initialUpdatedAt.After(first))

	require.NoError(t, handler.UpsertEventV3(ctx, namespace, eventContext, name, "second", []byte(`{"value":2}`), second))

	var source string
	var details []byte
	var timestamp, createdAt, updatedAt time.Time
	require.NoError(t, session.Query(`SELECT source, details, timestamp, created_at, updated_at FROM events_v3 WHERE namespace = ? AND context = ? AND event_name = ?`,
		namespace, eventContext, name).Scan(&source, &details, &timestamp, &createdAt, &updatedAt))
	assert.Equal(t, "second", source)
	assert.JSONEq(t, `{"value":2}`, string(details))
	assert.Equal(t, second.UTC(), timestamp.UTC().Truncate(time.Millisecond))
	assert.Equal(t, first.UTC(), createdAt.UTC().Truncate(time.Millisecond))
	assert.True(t, !updatedAt.Before(initialUpdatedAt))
}

func TestUpsertStatsV3KeepsLatestEvent(t *testing.T) {
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}
	ctx := logging.AttachLoggerToContext(context.Background(), otelzap.New(zap.NewNop()))
	handler := &CassandraHandler{session: session}
	const namespace, eventContext = "single-stats-upsert", "ctx-1"
	t.Cleanup(func() {
		_ = session.Query(`DELETE FROM stats_v3 WHERE namespace = ? AND context = ?`, namespace, eventContext).Exec()
	})
	first := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	latest := first.Add(2 * time.Minute)
	require.NoError(t, handler.UpsertStatsV3(ctx, namespace, eventContext, "pending", first))
	require.NoError(t, handler.UpsertStatsV3(ctx, namespace, eventContext, "ready", latest))
	require.NoError(t, handler.UpsertStatsV3(ctx, namespace, eventContext, "stale", first.Add(time.Minute)))
	require.NoError(t, handler.UpsertStatsV3(ctx, namespace, eventContext, "equal", latest))

	var name string
	var timestamp, createdAt time.Time
	require.NoError(t, session.Query(`SELECT event_name, timestamp, created_at FROM stats_v3 WHERE namespace = ? AND context = ?`,
		namespace, eventContext).Scan(&name, &timestamp, &createdAt))
	assert.Equal(t, "ready", name)
	assert.Equal(t, latest.UTC(), timestamp.UTC().Truncate(time.Millisecond))
	assert.Equal(t, first.UTC(), createdAt.UTC().Truncate(time.Millisecond))
}

func TestBulkUpsertEventsV3(t *testing.T) {
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}

	handler := &CassandraHandler{session: session}
	logger := otelzap.New(zap.NewNop())
	ctx := logging.AttachLoggerToContext(context.Background(), logger)

	_ = session.Query("TRUNCATE events_v3").Exec()
	t.Cleanup(func() { _ = session.Query("TRUNCATE events_v3").Exec() })

	now := time.Now().Truncate(time.Millisecond)

	t.Run("inserts multiple events across partitions", func(t *testing.T) {
		events := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-bulk", Context: "ctx-1", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: now},
			{Namespace: "ns-bulk", Context: "ctx-1", EventName: "pod.pending", Source: "test", Details: []byte(`{}`), Timestamp: now},
			{Namespace: "ns-bulk", Context: "ctx-2", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: now},
		}

		err := handler.BulkUpsertEventsV3(ctx, events)
		require.NoError(t, err)

		var count int
		err = session.Query(`SELECT COUNT(*) FROM events_v3 WHERE namespace = ? AND context = ?`, "ns-bulk", "ctx-1").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "ctx-1 should have 2 events")

		err = session.Query(`SELECT COUNT(*) FROM events_v3 WHERE namespace = ? AND context = ?`, "ns-bulk", "ctx-2").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "ctx-2 should have 1 event")
	})

	t.Run("preserves created_at on update", func(t *testing.T) {
		_ = session.Query("TRUNCATE events_v3").Exec()

		original := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
		first := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-ca", Context: "ctx-1", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: original},
		}
		require.NoError(t, handler.BulkUpsertEventsV3(ctx, first))

		updated := time.Now().Truncate(time.Millisecond)
		second := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-ca", Context: "ctx-1", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: updated},
		}
		require.NoError(t, handler.BulkUpsertEventsV3(ctx, second))

		var createdAt time.Time
		err := session.Query(`SELECT created_at FROM events_v3 WHERE namespace = ? AND context = ? AND event_name = ?`,
			"ns-ca", "ctx-1", "pod.ready").Scan(&createdAt)
		require.NoError(t, err)
		assert.Equal(t, original.UTC(), createdAt.UTC().Truncate(time.Millisecond), "created_at should remain from first insert")
	})

	t.Run("empty input is a no-op", func(t *testing.T) {
		err := handler.BulkUpsertEventsV3(ctx, nil)
		assert.NoError(t, err)
	})
}

func TestBulkUpsertStatsV3(t *testing.T) {
	session := getTestSession(t)
	if session == nil {
		t.Skip("Cassandra not available for testing")
	}

	handler := &CassandraHandler{session: session}
	logger := otelzap.New(zap.NewNop())
	ctx := logging.AttachLoggerToContext(context.Background(), logger)

	_ = session.Query("TRUNCATE stats_v3").Exec()
	t.Cleanup(func() { _ = session.Query("TRUNCATE stats_v3").Exec() })

	now := time.Now().Truncate(time.Millisecond)

	t.Run("inserts stats for multiple contexts", func(t *testing.T) {
		events := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-stats", Context: "ctx-1", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: now},
			{Namespace: "ns-stats", Context: "ctx-2", EventName: "pod.pending", Source: "test", Details: []byte(`{}`), Timestamp: now},
		}

		err := handler.BulkUpsertStatsV3(ctx, events)
		require.NoError(t, err)

		var count int
		err = session.Query(`SELECT COUNT(*) FROM stats_v3 WHERE namespace = ?`, "ns-stats").Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "should have one stats row per context")
	})

	t.Run("deduplicates to latest event per context", func(t *testing.T) {
		_ = session.Query("TRUNCATE stats_v3").Exec()

		older := now.Add(-time.Minute)
		events := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-dedup", Context: "ctx-1", EventName: "pod.pending", Source: "test", Details: []byte(`{}`), Timestamp: older},
			{Namespace: "ns-dedup", Context: "ctx-1", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: now},
		}

		err := handler.BulkUpsertStatsV3(ctx, events)
		require.NoError(t, err)

		var eventName string
		err = session.Query(`SELECT event_name FROM stats_v3 WHERE namespace = ? AND context = ?`, "ns-dedup", "ctx-1").Scan(&eventName)
		require.NoError(t, err)
		assert.Equal(t, "pod.ready", eventName, "should store the latest event_name")
	})

	t.Run("preserves created_at on update", func(t *testing.T) {
		_ = session.Query("TRUNCATE stats_v3").Exec()

		original := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
		first := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-sca", Context: "ctx-1", EventName: "pod.pending", Source: "test", Details: []byte(`{}`), Timestamp: original},
		}
		require.NoError(t, handler.BulkUpsertStatsV3(ctx, first))

		updated := time.Now().Truncate(time.Millisecond)
		second := []data_access.EventV3UpsertRecord{
			{Namespace: "ns-sca", Context: "ctx-1", EventName: "pod.ready", Source: "test", Details: []byte(`{}`), Timestamp: updated},
		}
		require.NoError(t, handler.BulkUpsertStatsV3(ctx, second))

		var createdAt time.Time
		err := session.Query(`SELECT created_at FROM stats_v3 WHERE namespace = ? AND context = ?`, "ns-sca", "ctx-1").Scan(&createdAt)
		require.NoError(t, err)
		assert.Equal(t, original.UTC(), createdAt.UTC().Truncate(time.Millisecond), "created_at should remain from first insert")
	})

	t.Run("empty input is a no-op", func(t *testing.T) {
		err := handler.BulkUpsertStatsV3(ctx, nil)
		assert.NoError(t, err)
	})
}
