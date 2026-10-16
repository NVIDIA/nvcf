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

package clustervalidator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// The Tier-2 row counts a terminating member as down. Where the pods cannot
// be listed, the StatefulSet's status still shows a member down, but not that
// the count it gives holds no terminating pod.
func TestCheckTier2StatefulSets_CountsMembersFromTheirPods(t *testing.T) {
	terminating := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	deleting := metav1.Now()
	podNamed(terminating, "nats-2").DeletionTimestamp = &deleting
	state, log := runTier2Logged(terminating)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
	assert.Contains(t, log, "nats-system/nats: Ready 2 of 3")

	deny := func(client *fake.Clientset) {
		client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(corev1.Resource("pods"), "", errors.New("denied"))
		})
	}
	nodes := []string{"node-1", "node-2", "node-3"}
	for name, tc := range map[string]struct {
		objs []runtime.Object
		want *bool
	}{
		"a member down":      {makeQuorumSTS("nats", "nats-system", 3, 2, nodes[:2]), cpRow(false)},
		"Ready and unspread": {withoutSpread(makeQuorumSTS("nats", "nats-system", 3, 3, nodes)), nil},
		"Ready and spread":   {makeQuorumSTS("nats", "nats-system", 3, 3, nodes), nil},
	} {
		client := fake.NewSimpleClientset(tc.objs...)
		deny(client)
		state := &ValidationState{Log: testLog()}
		checkTier2StatefulSets(context.Background(), client, state)
		assert.Equal(t, tc.want, state.Tier2StatefulSetsOK, name)
		if tc.want == nil {
			assert.Contains(t, strings.Join(state.Warnings, "; "), "could not read its pods", name)
		}
	}
}

// Tier-2 lists the pods of a namespace once, however many StatefulSets it
// assesses there.
func TestCheckTier2StatefulSets_ListsEachNamespacesPodsOnce(t *testing.T) {
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	objs = append(objs, makeQuorumSTS("jetstream", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})...)
	client := fake.NewSimpleClientset(objs...)
	checkTier2StatefulSets(context.Background(), client, &ValidationState{Log: testLog()})
	lists := map[string]int{}
	for _, a := range client.Actions() {
		if a.GetVerb() == "list" && a.GetResource().Resource == "pods" {
			lists[a.GetNamespace()]++
		}
	}
	assert.Equal(t, map[string]int{"nats-system": 1}, lists)
}

// During a tolerated rolling update the member being replaced may still be
// Ready while it terminates. It is not counted as a member, so sharing a node
// with another member is no co-location; the same pod not terminating is.
func TestCheckTier2StatefulSets_TerminatingMemberIsNoCoLocation(t *testing.T) {
	rolling := func(terminating bool) []runtime.Object {
		objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-2"})
		objs = rollTo(objs, "nats-r2", time.Now().Add(-time.Minute))
		if terminating {
			deleting := metav1.Now()
			podNamed(objs, "nats-2").DeletionTimestamp = &deleting
		}
		return objs
	}
	state, log := runTier2Logged(rolling(true))
	require.NotNil(t, state.Tier2StatefulSetsOK, log)
	assert.True(t, *state.Tier2StatefulSetsOK, log)
	assert.NotContains(t, log, "co-located")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nats-system/nats: "+RollingUpdateMarker+" (ready: 2/3)")

	state, log = runTier2Logged(rolling(false))
	require.NotNil(t, state.Tier2StatefulSetsOK, log)
	assert.False(t, *state.Tier2StatefulSetsOK, log)
	assert.Contains(t, log, "nats-system/nats: pods nats-1 and nats-2 are co-located on node node-2")
}
