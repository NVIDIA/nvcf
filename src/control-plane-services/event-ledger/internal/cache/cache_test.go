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

package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (h *CachingDBHandler) insert(k key, e entry) {
	h.entriesMu.Lock()
	defer h.entriesMu.Unlock()
	h.entries[k] = &e
}

func newTestHandler() *CachingDBHandler {
	return NewCachingDBHandler(nil, DefaultConfig())
}

func TestLookup_EmptyCacheMisses(t *testing.T) {
	h := newTestHandler()

	e, ok := h.lookup(key{namespace: "ns", context: "ctx", eventName: "evt"})

	assert.False(t, ok)
	assert.Equal(t, entry{}, e)
}

func TestLookup_AfterInsertHits(t *testing.T) {
	h := newTestHandler()
	k := key{namespace: "ns", context: "ctx", eventName: "evt"}
	ts := time.Date(2026, 9, 19, 14, 32, 10, 0, time.UTC)
	want := entry{
		timestamp:   ts,
		pending:     true,
		lastWritten: ts.Add(-time.Minute),
		lastUpdated: ts,
	}
	h.insert(k, want)

	got, ok := h.lookup(k)

	require.True(t, ok)
	assert.Equal(t, want, got)
}

func TestLookup_ReturnsCopy(t *testing.T) {
	h := newTestHandler()
	k := key{namespace: "ns", context: "ctx", eventName: "evt"}
	h.insert(k, entry{pending: true})

	got, ok := h.lookup(k)
	require.True(t, ok)
	got.pending = false

	again, ok := h.lookup(k)
	require.True(t, ok)
	assert.True(t, again.pending)
}

func TestLookup_KeysWithDifferentFieldsDoNotCollide(t *testing.T) {
	h := newTestHandler()
	base := key{namespace: "ns", context: "ctx", eventName: "evt"}
	h.insert(base, entry{})

	tests := map[string]key{
		"namespace": {namespace: "other", context: "ctx", eventName: "evt"},
		"context":   {namespace: "ns", context: "other", eventName: "evt"},
		"eventName": {namespace: "ns", context: "ctx", eventName: "other"},
		// Shifting a character across a field boundary must not alias.
		"boundary": {namespace: "nsc", context: "tx", eventName: "evt"},
	}
	for name, other := range tests {
		t.Run(name, func(t *testing.T) {
			_, ok := h.lookup(other)
			assert.False(t, ok)
		})
	}

	_, ok := h.lookup(base)
	assert.True(t, ok)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 100_000, cfg.MaxSize)
	assert.Equal(t, 60*time.Second, cfg.FlushInterval)
}

func TestInactiveTTL_IsFlushIntervalPlusTenPercent(t *testing.T) {
	tests := []struct {
		name          string
		flushInterval time.Duration
		want          time.Duration
	}{
		{name: "default", flushInterval: 60 * time.Second, want: 66 * time.Second},
		{name: "short interval", flushInterval: 10 * time.Second, want: 11 * time.Second},
		{name: "sub-second", flushInterval: 500 * time.Millisecond, want: 550 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Config{FlushInterval: tt.flushInterval}.inactiveTTL())
		})
	}
}

func TestNewCachingDBHandler_FillsZeroConfigWithDefaults(t *testing.T) {
	h := NewCachingDBHandler(nil, Config{})

	assert.Equal(t, DefaultConfig(), h.cfg)
}

func TestNewCachingDBHandler_KeepsExplicitConfig(t *testing.T) {
	cfg := Config{MaxSize: 10, FlushInterval: 5 * time.Second}

	h := NewCachingDBHandler(nil, cfg)

	assert.Equal(t, cfg, h.cfg)
}
