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

package progress

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowSink is a constrained EventSink implementation used to test the
// interface contract itself: terminal events bypass the (saturated) drop
// path; intermediate phase_progress events drop with a counter increment.
type slowSink struct {
	progressBuf     chan struct{}
	delivered       []Event
	droppedProgress int
}

func (s *slowSink) Emit(_ context.Context, e Event) error {
	if isTerminal(e) {
		s.delivered = append(s.delivered, e)
		return nil
	}
	select {
	case s.progressBuf <- struct{}{}:
		s.delivered = append(s.delivered, e)
	default:
		s.droppedProgress++
	}
	return nil
}

func (s *slowSink) Close() error { return nil }

func TestSink_TerminalEventsBypassDropPath(t *testing.T) {
	s := &slowSink{progressBuf: make(chan struct{}, 1)}
	ctx := context.Background()

	// Saturate progress buffer
	require.NoError(t, s.Emit(ctx, PhaseProgress{Num: 4, Done: 1, Total: 14}))
	// Next progress event drops
	require.NoError(t, s.Emit(ctx, PhaseProgress{Num: 4, Done: 2, Total: 14}))
	assert.Equal(t, 1, s.droppedProgress)

	// Terminal events ALWAYS delivered, even with progress saturation
	require.NoError(t, s.Emit(ctx, PhaseFailed{Num: 4, ErrCategory: "internal", ErrMessage: "boom"}))
	require.NoError(t, s.Emit(ctx, Final{Duration: 0}))

	assert.Len(t, s.delivered, 3) // 1 progress + phase_failed + final
}

func TestIsTerminal_ClassifiesEvents(t *testing.T) {
	assert.True(t, isTerminal(PhaseFailed{}))
	assert.True(t, isTerminal(PhaseCancelled{Reason: "sigint"}))
	assert.True(t, isTerminal(Final{}))
	assert.False(t, isTerminal(PhaseStarted{}))
	assert.False(t, isTerminal(PhaseProgress{}))
	assert.False(t, isTerminal(Waiting{}))
}
