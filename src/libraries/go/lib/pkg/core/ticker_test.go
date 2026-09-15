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
)

func TestTicker(t *testing.T) {
	ctx := context.Background()
	sources := []RecurrentEventSource{
		{
			Kind:     "TestEventSource",
			Interval: 2 * time.Second,
			Ticker:   nil,
		},
	}
	ticker := &RecurrentEventTicker{
		Sources: sources,
		C:       make(chan *Event),
	}
	go ticker.Start(ctx.Done())
	ticker.Stop()
}

// TestTicker_StopWithActiveTicker exercises the Stop() path where Ticker != nil.
func TestTicker_StopWithActiveTicker(t *testing.T) {
	activeTicker := time.NewTicker(time.Hour)
	sources := []RecurrentEventSource{
		{
			Kind:     "TestEventSource",
			Interval: time.Hour,
			Ticker:   activeTicker,
		},
	}
	ticker := &RecurrentEventTicker{
		Sources: sources,
		C:       make(chan *Event),
	}
	// Just ensure Stop() doesn't panic when Ticker != nil (covers lines 36-37)
	ticker.Stop()
}

// TestTicker_StartReceivesEvents exercises the for loop in Start() goroutine.
func TestTicker_StartReceivesEvents(t *testing.T) {
	sources := []RecurrentEventSource{
		{
			Kind:     "TestEventSource",
			Interval: 10 * time.Millisecond,
		},
	}
	ch := make(chan *Event, 100)
	ticker := &RecurrentEventTicker{
		Sources: sources,
		C:       ch,
	}
	stopCh := make(chan struct{})
	go ticker.Start(stopCh)

	// Read the initial event (dispatched before the for-loop ticker is set)
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial event")
	}

	// Read at least one tick event from the for-loop body
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tick event")
	}

	close(stopCh)
}
