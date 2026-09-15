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

package teardown

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted/progress"
)

// fakeSIS is a test double for FunctionDeploymentLister.
type fakeSIS struct {
	deployments   []ActiveDeployment
	removeErr     error
	removeCalled  []string // IDs passed to RemoveDeployment
	statusResults map[string]string // ID → state string
	statusCallCnt int32
}

func (f *fakeSIS) ListActiveDeployments(_ context.Context, _, _, _ string) ([]ActiveDeployment, error) {
	return f.deployments, nil
}

func (f *fakeSIS) RemoveDeployment(_ context.Context, _, _, _, id string) error {
	f.removeCalled = append(f.removeCalled, id)
	return f.removeErr
}

func (f *fakeSIS) DeploymentStatus(_ context.Context, _, _, _, id string) (string, error) {
	atomic.AddInt32(&f.statusCallCnt, 1)
	state, ok := f.statusResults[id]
	if !ok {
		return "", errors.New("not found")
	}
	return state, nil
}

// recordSink collects emitted events.
type recordSink struct {
	events []progress.Event
}

func (s *recordSink) Emit(_ context.Context, e progress.Event) error {
	s.events = append(s.events, e)
	return nil
}

func (s *recordSink) Close() error { return nil }

// discardSink drops all events (used in helmfile_destroy tests).
type discardSink struct{}

func (d *discardSink) Emit(_ context.Context, _ progress.Event) error { return nil }
func (d *discardSink) Close() error                                    { return nil }

// TestDrain_NoActiveDeploymentsIsNoop: empty list → returns nil, no events.
func TestDrain_NoActiveDeploymentsIsNoop(t *testing.T) {
	sis := &fakeSIS{deployments: nil}
	sink := &recordSink{}

	err := Drain(context.Background(), sis, "http://sis", "nca1", "cl1", DrainModeForce, sink)
	require.NoError(t, err)
	assert.Empty(t, sink.events)
	assert.Empty(t, sis.removeCalled)
}

// TestDrain_ForceModePollsUntilStopped: 2 deployments removed + polled to STOPPED.
func TestDrain_ForceModePollsUntilStopped(t *testing.T) {
	sis := &fakeSIS{
		deployments: []ActiveDeployment{
			{ID: "d1", Name: "fn-a", Version: "3"},
			{ID: "d2", Name: "fn-b", Version: "1"},
		},
		statusResults: map[string]string{
			"d1": "STOPPED",
			"d2": "STOPPED",
		},
	}
	sink := &recordSink{}

	err := Drain(context.Background(), sis, "http://sis", "nca1", "cl1", DrainModeForce, sink)
	require.NoError(t, err)

	// 2 RemoveDeployment calls
	assert.Len(t, sis.removeCalled, 2)
	assert.ElementsMatch(t, []string{"d1", "d2"}, sis.removeCalled)

	// Events: 2 REMOVING + 2 STOPPED
	require.Len(t, sink.events, 4)
	removingStates := map[string]int{}
	stoppedStates := map[string]int{}
	for _, ev := range sink.events {
		dp, ok := ev.(progress.DrainProgress)
		require.True(t, ok, "expected DrainProgress, got %T", ev)
		switch dp.State {
		case "REMOVING":
			removingStates[dp.Deployment]++
		case "STOPPED":
			stoppedStates[dp.Deployment]++
		}
	}
	assert.Equal(t, map[string]int{"fn-a": 1, "fn-b": 1}, removingStates)
	assert.Equal(t, map[string]int{"fn-a": 1, "fn-b": 1}, stoppedStates)
}

// TestDrain_SkipMode: deployments present but mode=Skip → Waiting event, no remove calls.
func TestDrain_SkipMode(t *testing.T) {
	sis := &fakeSIS{
		deployments: []ActiveDeployment{
			{ID: "d1", Name: "fn-a", Version: "3"},
			{ID: "d2", Name: "fn-b", Version: "1"},
		},
	}
	sink := &recordSink{}

	err := Drain(context.Background(), sis, "http://sis", "nca1", "cl1", DrainModeSkip, sink)
	require.NoError(t, err)
	assert.Empty(t, sis.removeCalled)
	require.Len(t, sink.events, 1)
	_, ok := sink.events[0].(progress.Waiting)
	assert.True(t, ok, "expected Waiting event, got %T", sink.events[0])
}

// TestDrain_TimeoutReturnsErr: status never flips to STOPPED → ErrDrainTimeout.
func TestDrain_TimeoutReturnsErr(t *testing.T) {
	sis := &fakeSIS{
		deployments: []ActiveDeployment{
			{ID: "d1", Name: "fn-stuck", Version: "1"},
		},
		statusResults: map[string]string{
			"d1": "REMOVING", // never transitions
		},
	}
	sink := &recordSink{}

	// Override the package-level drainTimeout to a very short window so the
	// test completes quickly without sleeping for 5 minutes.
	orig := drainTimeout
	drainTimeout = 200 * time.Millisecond
	t.Cleanup(func() { drainTimeout = orig })

	err := Drain(context.Background(), sis, "http://sis", "nca1", "cl1", DrainModeForce, sink)
	assert.ErrorIs(t, err, ErrDrainTimeout)
}
