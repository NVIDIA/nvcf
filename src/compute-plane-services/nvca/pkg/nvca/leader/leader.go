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
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const (
	leaseLockName      = "nvcluster-agent-leader-lock"
	leaseDurationInSec = 15
	renewDeadlineInSec = 10
	retryPeriodInSec   = 2
)

// leaderElection ...
type Election struct {
	leaderElector *leaderelection.LeaderElector
	lock          *resourcelock.LeaseLock
	identity      string
	isLeaderWg    sync.WaitGroup
	lostLeaderCh  chan struct{}
}

func NewLeaderElection(leaseLockNamespace string, client k8sclient.Interface,
	identity string) *Election {
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: identity,
		},
	}
	le := Election{
		lock:         lock,
		identity:     identity,
		lostLeaderCh: make(chan struct{}),
		isLeaderWg:   sync.WaitGroup{},
	}
	return &le
}

func (le *Election) getNewLeaderElector() (*leaderelection.LeaderElector, error) {
	leaderElector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		// Add required config parameters
		Lock:          le.lock,
		LeaseDuration: leaseDurationInSec * time.Second,
		RenewDeadline: renewDeadlineInSec * time.Second,
		RetryPeriod:   retryPeriodInSec * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: le.onStartedLeading,
			OnStoppedLeading: le.onStoppedLeading,
		},
	})
	if err != nil {
		return nil, err
	}
	return leaderElector, nil
}

func (le *Election) onStartedLeading(ctx context.Context) {
	log := core.GetLogger(ctx)
	le.isLeaderWg.Done()
	log.Debugf("Acquired leader lease with pod identity : %s", le.identity)
}

func (le *Election) onStoppedLeading() {
	close(le.lostLeaderCh)
}

// Run...
func (le *Election) Run(ctx context.Context) error {
	log := core.GetLogger(ctx)
	log.Debugf("Running leader election process with pod identity : %s", le.identity)
	le.lostLeaderCh = make(chan struct{})
	le.isLeaderWg.Add(1)
	var err error
	le.leaderElector, err = le.getNewLeaderElector()
	if err != nil {
		return err
	}
	go le.leaderElector.Run(ctx)
	return nil
}

// IsLeader returns true if the current instance is leader
func (le *Election) IsLeader() bool {
	return le.leaderElector.IsLeader()
}

// BlockUntilLeader blocks until leader lease is acquired
func (le *Election) BlockUntilLeader() {
	le.isLeaderWg.Wait()
}

// BlockUntilLostLeader blocks until leader.
// This method should be called after Run
func (le *Election) BlockUntilLostLeader() chan struct{} {
	return le.lostLeaderCh
}

func (le *Election) Close() {
	close(le.lostLeaderCh)
}
