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

package leader

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
)

func TestNewLeaderElection(t *testing.T) {

	ctx := context.Background()

	client := fakek8sclient.NewSimpleClientset()
	le := NewLeaderElection("bart-system", client, uuid.NewString())

	err := le.Run(ctx)
	if err != nil {
		t.Error("Failed to run the leader election process", err)
	}
	le.BlockUntilLeader()
	if !le.IsLeader() {
		t.Error("Leadership not acquired")
	}
}

func TestNewLeaderElectionWithTwoInstances(t *testing.T) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	client := fakek8sclient.NewSimpleClientset()

	le1 := NewLeaderElection("bart-system1", client, uuid.NewString())
	le2 := NewLeaderElection("bart-system1", client, uuid.NewString())
	wg := &sync.WaitGroup{}
	// Either instance should get the lease
	wg.Add(1)
	runElection(ctx, t, le1, wg)
	runElection(ctx, t, le2, wg)
	go blockUntilElection(ctx, t, le1, wg)
	go blockUntilElection(ctx, t, le2, wg)

	// Wait for instance to acquire lease else timeout
	wg.Wait()

	// Assert that both instances have not acquired lease
	assert.NotEqual(t, le1.IsLeader(), le2.IsLeader())

	var leaseLeader *Election
	if le1.IsLeader() {
		leaseLeader = le1
	} else {
		leaseLeader = le2
	}

	// Test if cancelling context releases the leader lease
	// Block Until Leader
	blockWg := &sync.WaitGroup{}
	blockWg.Add(1)
	go func() {
		leaseLeader.BlockUntilLostLeader()
		blockWg.Done()
	}()
	cancelFunc()
	blockWg.Wait()
}

func runElection(ctx context.Context, t *testing.T, le *Election, wg *sync.WaitGroup) {
	err := le.Run(ctx)
	if err != nil {
		t.Error("Failed to run the leader election process", err)
	}
}

func blockUntilElection(ctx context.Context, t *testing.T, le *Election, wg *sync.WaitGroup) {
	le.BlockUntilLeader()
	if !le.IsLeader() {
		t.Error("Leadership not acquired")
		return
	}
	wg.Done()
}
