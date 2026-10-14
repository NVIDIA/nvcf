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
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// requireSpread makes the fixture's hostname anti-affinity required, as mode
// enforced renders it.
func requireSpread(objs []runtime.Object) []runtime.Object {
	aa := objs[0].(*appsv1.StatefulSet).Spec.Template.Spec.Affinity.PodAntiAffinity
	for _, w := range aa.PreferredDuringSchedulingIgnoredDuringExecution {
		aa.RequiredDuringSchedulingIgnoredDuringExecution = append(aa.RequiredDuringSchedulingIgnoredDuringExecution,
			w.PodAffinityTerm)
	}
	aa.PreferredDuringSchedulingIgnoredDuringExecution = nil
	return objs
}

// asTableWorkload labels the fixture's pod template as release's, the quorum
// workload of haWorkloads the HA Guarantee row finds by those labels.
func asTableWorkload(release string, objs []runtime.Object) []runtime.Object {
	tpl := &objs[0].(*appsv1.StatefulSet).Spec.Template
	tpl.Labels = maps.Clone(tpl.Labels)
	for _, w := range haWorkloads {
		if w.release == release {
			maps.Copy(tpl.Labels, w.selector())
		}
	}
	return objs
}

// runTier2In runs Tier-2 for a run given mode and returns its state and log.
func runTier2In(mode HAMode, objs []runtime.Object) (*ValidationState, string) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	state := &ValidationState{Log: logrus.NewEntry(l), HAMode: mode}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(objs...), state)
	return state, buf.String()
}

// Two Ready quorum peers on one node fail the Tier-2 row only where the
// placement asked for is hard: any anti-affinity under mode none, as before HA
// modes existed, or a required term when no mode was supplied. A preferred
// term without a mode allows co-location when nodes are short, so it is a
// failover-risk warning. Under preferred or enforced the HA Guarantee row
// judges the placement of the stack's own quorum members, so the Tier-2 row
// does not.
func TestCheckTier2StatefulSets_CoLocationFollowsTheHAMode(t *testing.T) {
	coLocated := func() []runtime.Object {
		return makeQuorumSTS("cassandra", "cassandra-system", 3, 3, []string{"node-1", "node-1", "node-2"})
	}
	const (
		fails = iota
		warns
		delegated
	)
	for name, tc := range map[string]struct {
		mode HAMode
		objs []runtime.Object
		want int
	}{
		"mode none, preferred term":    {HAModeNone, coLocated(), fails},
		"mode none, required term":     {HAModeNone, requireSpread(coLocated()), fails},
		"not supplied, required term":  {"", requireSpread(coLocated()), fails},
		"not supplied, preferred term": {"", coLocated(), warns},
		"mode preferred":               {HAModePreferred, asTableWorkload("cassandra", coLocated()), delegated},
		"mode enforced": {HAModeEnforced, asTableWorkload("cassandra", requireSpread(coLocated())),
			delegated},
		"mode enforced, not the release's labels":  {HAModeEnforced, requireSpread(coLocated()), fails},
		"mode preferred, not the release's labels": {HAModePreferred, coLocated(), warns},
	} {
		state, log := runTier2In(tc.mode, tc.objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		warnings := strings.Join(state.Warnings, "; ")
		switch tc.want {
		case fails:
			assert.False(t, *state.Tier2StatefulSetsOK, name)
			assert.Equal(t, []string{tier2CoLocatedAdvice}, state.Recommendations, name)
		case warns:
			assert.True(t, *state.Tier2StatefulSetsOK, name)
			assert.Empty(t, state.Recommendations, name)
			assert.Contains(t, warnings, "Tier-2 StatefulSets: failover risk: cassandra-system/cassandra pods "+
				"cassandra-0 and cassandra-1 share node node-1", name)
			assert.Equal(t, 1, state.Tier2PeersSharingANode, name)
			assert.NotContains(t, log, "on distinct nodes", name)
			for _, marker := range cliRolloutMarkers {
				assert.NotContains(t, warnings, marker, "%s: not a rollout to wait out", name)
			}
		case delegated:
			assert.True(t, *state.Tier2StatefulSetsOK, name)
			assert.Empty(t, state.Recommendations, name)
			assert.NotContains(t, warnings, "share node", name)
			assert.Zero(t, state.Tier2PeersSharingANode, name)
			assert.Zero(t, state.Tier2PlacementNotAssessed, name)
			assert.Equal(t, 1, state.Tier2PlacementDelegated, name)
			assert.Contains(t, log, "All 1 quorum StatefulSet(s) Ready (placement of cassandra-system/cassandra "+
				"judged under HA Guarantee)", name)
		}
	}
}

// Under an HA mode the Tier-2 row lists none of the stack's quorum members as
// unspread: the HA Guarantee row reports missing anti-affinity, and the row
// keeps judging quorum and readiness as before.
func TestCheckTier2StatefulSets_HAModeKeepsQuorumAndReadiness(t *testing.T) {
	for _, mode := range []HAMode{HAModePreferred, HAModeEnforced} {
		state, _ := runTier2In(mode, asTableWorkload("nats", withoutSpread(makeQuorumSTS("nats", "nats-system", 3, 3,
			[]string{"node-1", "node-1", "node-1"}))))
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.True(t, *state.Tier2StatefulSetsOK, mode)
		assert.Zero(t, state.Tier2PlacementNotAssessed, mode)

		state, _ = runTier2In(mode, makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"}))
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.False(t, *state.Tier2StatefulSetsOK, "%s: a member down outside a rollout still fails", mode)

		state, _ = runTier2In(mode, makeQuorumSTS("cassandra", "cassandra-system", 1, 1, []string{"node-1"}))
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.False(t, *state.Tier2StatefulSetsOK, "%s: a spread component below quorum still fails", mode)
	}
}

// The Tier-2 row says how it judged placement: for the stack's quorum members
// under an HA mode the HA Guarantee row did, and a preferred-only placement
// that let peers share a node is not reported as a clean placement.
func TestPrintSummary_Tier2RowNamesHowPlacementWasJudged(t *testing.T) {
	for _, tc := range []struct {
		mode               HAMode
		sharing, delegated int
		want, avoid        string
	}{
		{HAModePreferred, 0, 3, "Tier-2 StatefulSets: Quorum OK (placement of 3 judged under HA Guarantee)",
			"Quorum and Placement OK"},
		{HAModePreferred, 1, 3, "Tier-2 StatefulSets: Quorum OK (1 with peers sharing a node; see warnings)",
			"Quorum and Placement OK"},
		{HAModeEnforced, 0, 0, "Tier-2 StatefulSets: Quorum and Placement OK", "judged under"},
		{"", 2, 0, "Tier-2 StatefulSets: Quorum OK (2 with peers sharing a node; see warnings)",
			"Quorum and Placement OK"},
		{"", 0, 0, "Tier-2 StatefulSets: Quorum and Placement OK", "sharing a node"},
	} {
		buf := &bytes.Buffer{}
		l := logrus.New()
		l.SetOutput(buf)
		state := healthyControlPlaneState()
		state.Log, state.HAMode, state.Tier2PeersSharingANode = logrus.NewEntry(l), tc.mode, tc.sharing
		state.Tier2PlacementDelegated = tc.delegated
		state.HAGuaranteeOK = cpRow(true)
		_ = printSummary(state)
		assert.Contains(t, buf.String(), tc.want)
		assert.NotContains(t, buf.String(), tc.avoid)
	}
}

// Under an HA mode the HA Guarantee row judges only the quorum members of the
// workload table. Any other quorum StatefulSet the Tier-2 row assesses keeps
// the judgement its own rendered terms ask for, so peers on one node still
// fail it under a required term, and the row names only the members it hands
// over.
func TestCheckTier2StatefulSets_HAModeDelegatesOnlyTheStackQuorum(t *testing.T) {
	coLocated := func() []runtime.Object {
		return makeQuorumSTS("redis-ha", "nvcf", 3, 3, []string{"node-1", "node-1", "node-2"})
	}
	for _, mode := range []HAMode{HAModePreferred, HAModeEnforced} {
		state, _ := runTier2In(mode, requireSpread(coLocated()))
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.False(t, *state.Tier2StatefulSetsOK, "%s: a required term outside the table still fails", mode)
		assert.Equal(t, []string{tier2CoLocatedAdvice}, state.Recommendations, mode)

		state, _ = runTier2In(mode, coLocated())
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.True(t, *state.Tier2StatefulSetsOK, mode)
		assert.Contains(t, strings.Join(state.Warnings, "; "), "Tier-2 StatefulSets: failover risk: nvcf/redis-ha "+
			"pods redis-ha-0 and redis-ha-1 share node node-1", mode)
		assert.Zero(t, state.Tier2PlacementDelegated, mode)

		objs := asTableWorkload("nats", makeQuorumSTS("nats", "nats-system", 3, 3,
			[]string{"node-1", "node-1", "node-1"}))
		objs = append(objs, makeQuorumSTS("redis-ha", "nvcf", 3, 3, []string{"node-1", "node-2", "node-3"})...)
		state, log := runTier2In(mode, objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.True(t, *state.Tier2StatefulSetsOK, mode)
		assert.Equal(t, 1, state.Tier2PlacementDelegated, mode)
		assert.Contains(t, log, "All 2 quorum StatefulSet(s) Ready (1 on distinct nodes; placement of "+
			"nats-system/nats judged under HA Guarantee)", mode)
		assert.Equal(t, "Quorum OK (placement of 1 judged under HA Guarantee)", tier2PassMessage(state), mode)
	}
}

// A quorum member whose pod is terminating is not Ready, though the
// StatefulSet's status counts it until the kubelet stops it. The Tier-2 and HA
// Guarantee rows count members alike, from the pods they list, so one run
// cannot report a quorum both whole and a member down.
func TestTier2AndHAGuarantee_CountATerminatingMemberAlike(t *testing.T) {
	for _, mode := range []HAMode{HAModePreferred, HAModeEnforced} {
		f := newHAFixture(t, mode)
		deleting := metav1.Now()
		f.pods["nats"][0].DeletionTimestamp = &deleting
		require.Equal(t, int32(3), f.sets["nats"].Status.ReadyReplicas)
		client := fake.NewSimpleClientset(f.objects()...)

		tier2, tier2Log := bufferLog()
		state := &ValidationState{Log: tier2, HAMode: mode, PostInstall: true}
		checkTier2StatefulSets(context.Background(), client, state)
		require.NotNil(t, state.Tier2StatefulSetsOK, mode)
		assert.False(t, *state.Tier2StatefulSetsOK, mode)
		assert.Contains(t, tier2Log.String(), "nats-system/nats: Ready 2 of 3", mode)

		ha, haLog := bufferLog()
		state = &ValidationState{Log: ha, HAMode: mode, PostInstall: true}
		checkHAGuarantee(context.Background(), client, state)
		require.NotNil(t, state.HAGuaranteeOK, mode)
		assert.False(t, *state.HAGuaranteeOK, mode)
		assert.Contains(t, haLog.String(), "nats-system/nats: Ready 2 of 3, mode "+string(mode)+
			" requires every member Ready", mode)
	}
}

// Without an HA mode the Tier-2 row counts a terminating member as down too.
// Where the pods cannot be listed, the StatefulSet's status still shows a
// member down, but not that the count it gives holds no terminating pod.
func TestCheckTier2StatefulSets_CountsMembersFromTheirPods(t *testing.T) {
	terminating := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-2", "node-3"})
	deleting := metav1.Now()
	podNamed(terminating, "nats-2").DeletionTimestamp = &deleting
	state, log := runTier2In("", terminating)
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
	state, log := runTier2In(HAModeNone, rolling(true))
	require.NotNil(t, state.Tier2StatefulSetsOK, log)
	assert.True(t, *state.Tier2StatefulSetsOK, log)
	assert.NotContains(t, log, "co-located")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nats-system/nats: "+RollingUpdateMarker+" (ready: 2/3)")

	state, log = runTier2In(HAModeNone, rolling(false))
	require.NotNil(t, state.Tier2StatefulSetsOK, log)
	assert.False(t, *state.Tier2StatefulSetsOK, log)
	assert.Contains(t, log, "nats-system/nats: pods nats-1 and nats-2 are co-located on node node-2")
}
