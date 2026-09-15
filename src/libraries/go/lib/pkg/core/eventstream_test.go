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

package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEventStreamWithMerge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
	t.Cleanup(cancel)
	ctx = WithDefaultLogger(ctx)
	ctx = WithRandomSeed(ctx, 42)

	sb := NewTickerStream().
		WithBufferSize(0).
		WithImmediate(true).
		WithRandomOffset(true)

	s1 := sb.
		WithKind("A").
		WithInterval(200 * time.Millisecond).
		Start(ctx)

	s2 := sb.
		WithKind("B").
		WithInterval(400 * time.Millisecond).
		Start(ctx)

	s3 := sb.
		WithKind("C").
		WithInterval(600 * time.Millisecond).
		Start(ctx)

	s := NewEventStreamMerger().
		WithBufferSize(100).
		Merge(ctx, s1, s2, s3)

	log := GetLogger(ctx)
	events := []string{}
	for ev := range s {
		events = append(events, ev.Kind)
		log.Infof("event: %s", ev)
	}

	expected := []string{"B", "A", "A", "C", "B", "A", "A", "B", "A", "C"}
	assert.Equal(t, expected, events[3:])
}

func TestNewUpdateEvent(t *testing.T) {
	oldObj := struct{ Text string }{"Hello world!"}
	newObj := struct{ Text string }{"Hello new world!"}
	assert.NotNil(t, NewUpdateEvent("interface", oldObj, newObj))
}

func TestEventStreamWithMergeWithZeroEventTimestamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Millisecond)
	t.Cleanup(cancel)
	ctx = WithDefaultLogger(ctx)
	ctx = WithRandomSeed(ctx, 42)

	sb := NewTickerStream().
		WithBufferSize(0).
		WithImmediate(true).
		WithRandomOffset(true)

	s1 := sb.
		WithKind("A").
		WithInterval(200 * time.Millisecond).
		WithZeroEventTimestamp(true).
		Start(ctx)

	s2 := sb.
		WithKind("B").
		WithInterval(400 * time.Millisecond).
		WithZeroEventTimestamp(true).
		Start(ctx)

	s3 := sb.
		WithKind("C").
		WithInterval(600 * time.Millisecond).
		WithZeroEventTimestamp(true).
		Start(ctx)

	s := NewEventStreamMerger().
		WithBufferSize(100).
		Merge(ctx, s1, s2, s3)

	log := GetLogger(ctx)
	events := []string{}
	for ev := range s {
		events = append(events, ev.Kind)
		assert.Equal(t, &time.Time{}, ev.Object)
		log.Infof("event: %s", ev)
	}

	expected := []string{"B", "A", "A", "C", "B", "A", "A", "B", "A", "C"}
	assert.Equal(t, expected, events[3:])
}
