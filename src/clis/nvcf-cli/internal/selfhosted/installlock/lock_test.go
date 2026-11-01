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

package installlock_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"nvcf-cli/internal/selfhosted/installlock"
)

// TestLock_Acquire_FirstCallSucceeds verifies that the first Acquire on a key
// succeeds and the lock can be cleanly released.
func TestLock_Acquire_FirstCallSucceeds(t *testing.T) {
	kube := fake.NewSimpleClientset()
	l := installlock.NewLock(kube, "test-key", installlock.Options{})
	require.NoError(t, l.Acquire(context.Background()))
	require.NoError(t, l.Release(context.Background()))
}

// TestLock_Acquire_SecondConcurrentReturnsErrAlreadyHeld verifies that a
// second Acquire on the same key while the first is still held returns
// ErrAlreadyHeld containing the first holder's identity.
func TestLock_Acquire_SecondConcurrentReturnsErrAlreadyHeld(t *testing.T) {
	kube := fake.NewSimpleClientset()

	aliceStart := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	a := installlock.NewLock(kube, "test-key", installlock.Options{
		Holder: installlock.HolderIdentity{Hostname: "alice", PID: 100, Started: aliceStart},
	})
	require.NoError(t, a.Acquire(context.Background()))
	defer func() { _ = a.Release(context.Background()) }()

	b := installlock.NewLock(kube, "test-key", installlock.Options{
		Holder: installlock.HolderIdentity{Hostname: "bob", PID: 200, Started: time.Now()},
	})
	err := b.Acquire(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, installlock.ErrAlreadyHeld, "error should wrap ErrAlreadyHeld")
	// Error message must include alice's hostname and PID so operators can identify the holder.
	assert.Contains(t, err.Error(), "alice pid=100", "error message should contain alice's identity")
}

// TestLock_Release_AllowsReacquisition verifies that after Release the key is
// available for a second caller.
func TestLock_Release_AllowsReacquisition(t *testing.T) {
	kube := fake.NewSimpleClientset()

	a := installlock.NewLock(kube, "test-key", installlock.Options{})
	require.NoError(t, a.Acquire(context.Background()))
	require.NoError(t, a.Release(context.Background()))

	b := installlock.NewLock(kube, "test-key", installlock.Options{})
	require.NoError(t, b.Acquire(context.Background()), "reacquisition after release should succeed")
	defer func() { _ = b.Release(context.Background()) }()
}

// TestLock_ExpiredLease_ReacquirableByNewHolder verifies that when the clock
// advances past LeaseTTL the expired lease can be claimed by a new holder
// (crash-recovery path — the original holder died without releasing).
func TestLock_ExpiredLease_ReacquirableByNewHolder(t *testing.T) {
	kube := fake.NewSimpleClientset()

	fakeClock := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return fakeClock }

	a := installlock.NewLock(kube, "test-key", installlock.Options{
		LeaseTTL: 1 * time.Minute,
		NowFunc:  nowFn,
	})
	require.NoError(t, a.Acquire(context.Background()))
	// Simulate process death: do NOT release.

	// Advance clock past TTL.
	fakeClock = fakeClock.Add(2 * time.Minute)

	b := installlock.NewLock(kube, "test-key", installlock.Options{
		LeaseTTL: 1 * time.Minute,
		NowFunc:  nowFn,
	})
	err := b.Acquire(context.Background())
	require.NoError(t, err, "expired lease should be re-acquirable by a new holder")
	defer func() { _ = b.Release(context.Background()) }()
}

// TestLock_DifferentKeysDontConflict verifies that two locks on distinct keys
// can both be acquired simultaneously without conflict.
func TestLock_DifferentKeysDontConflict(t *testing.T) {
	kube := fake.NewSimpleClientset()

	a := installlock.NewLock(kube, "key-a", installlock.Options{})
	b := installlock.NewLock(kube, "key-b", installlock.Options{})

	require.NoError(t, a.Acquire(context.Background()))
	defer func() { _ = a.Release(context.Background()) }()

	require.NoError(t, b.Acquire(context.Background()), "distinct keys must not conflict")
	defer func() { _ = b.Release(context.Background()) }()
}
