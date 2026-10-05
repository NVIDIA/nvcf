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
	"fmt"
	"math"
	"slices"
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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// installedAt is when fixtures were installed: long enough ago that the
// install itself is no recent rollout progress.
var installedAt = time.Now().Add(-24 * time.Hour)

// installManager is the field manager that installed and upgrades fixtures.
const installManager = "helm"

// specWrite is the managedFields entry a write to an object's spec by manager
// leaves, at t.
func specWrite(manager string, t time.Time) metav1.ManagedFieldsEntry {
	return managedFields(manager, "", `{"f:metadata":{"f:labels":{}},"f:spec":{"f:template":{}}}`, t)
}

// statusWrite is the entry a controller's status update leaves, at t.
func statusWrite(t time.Time) metav1.ManagedFieldsEntry {
	return managedFields("kube-controller-manager", "status", `{"f:status":{"f:readyReplicas":{}}}`, t)
}

func managedFields(manager, subresource, fields string, t time.Time) metav1.ManagedFieldsEntry {
	at := metav1.NewTime(t)
	return metav1.ManagedFieldsEntry{
		Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "apps/v1", Time: &at,
		FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(fields)}, Subresource: subresource,
	}
}

// rollTo starts a rollout of the StatefulSet in objs to revision rev at
// started: the upgrade that writes its spec, and the ControllerRevision the
// controller creates for it. It re-dates both when rev already exists, which
// is also what a rollback to rev looks like.
func rollTo(objs []runtime.Object, rev string, started time.Time) []runtime.Object {
	sts := objs[0].(*appsv1.StatefulSet)
	sts.Status.UpdateRevision = rev
	sts.Status.UpdatedReplicas = 0
	for i := range sts.ManagedFields {
		if sts.ManagedFields[i].Manager == installManager {
			sts.ManagedFields[i] = specWrite(installManager, started)
		}
	}
	at := metav1.NewTime(started)
	for _, o := range objs {
		if cr, ok := o.(*appsv1.ControllerRevision); ok && cr.Name == rev {
			cr.CreationTimestamp = at
			return objs
		}
	}
	return append(objs, &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Name: rev, Namespace: sts.Namespace, CreationTimestamp: at},
		Revision:   2,
	})
}

// withoutRevision drops ControllerRevision rev, so it cannot be read.
func withoutRevision(objs []runtime.Object, rev string) []runtime.Object {
	return slices.DeleteFunc(objs, func(o runtime.Object) bool {
		cr, ok := o.(*appsv1.ControllerRevision)
		return ok && cr.Name == rev
	})
}

// unrecorded drops the StatefulSet's managedFields, leaving its creation and
// its revisions to date a rollout.
func unrecorded(objs []runtime.Object) []runtime.Object {
	objs[0].(*appsv1.StatefulSet).ManagedFields = nil
	return objs
}

// waiting puts a pod's container in the given waiting reason.
func waiting(reason string) func(*corev1.Pod) {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "nats", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
		}}
	}
}

// runTier2Logged runs Tier-2 and returns its log as well.
func runTier2Logged(objs []runtime.Object) (*ValidationState, string) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	state := &ValidationState{Log: logrus.NewEntry(l)}
	checkTier2StatefulSets(context.Background(), fake.NewSimpleClientset(objs...), state)
	return state, buf.String()
}

// cliRolloutMarkers are the warning texts the CLI's --wait reads as a rollout
// still moving, so it keeps waiting.
var cliRolloutMarkers = []string{"rollout in progress", "rolling update in progress", "mid-rollout"}

// -- Tier-2 rollout bound --

// Pod states that clear on their own during a healthy upgrade ride the time
// bound instead of failing at once: a pod waiting for the autoscaler or a
// zonal volume, a registry briefly refusing a pull, a crash during peer
// discovery, a Secret not yet applied. Past the bound each fails.
func TestCheckTier2StatefulSets_TransientPodStatesRideTheBound(t *testing.T) {
	states := map[string]func(*corev1.Pod){
		"unschedulable": func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
			}}
		},
		"ErrImagePull":               waiting("ErrImagePull"),
		"ImagePullBackOff":           waiting("ImagePullBackOff"),
		"CrashLoopBackOff":           waiting("CrashLoopBackOff"),
		"CreateContainerConfigError": waiting("CreateContainerConfigError"),
		"restarting": func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "nats", RestartCount: 5}}
		},
	}
	stale := time.Now().Add(-16 * time.Minute)
	for name, mutate := range states {
		state := runTier2(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", mutate))
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.True(t, *state.Tier2StatefulSetsOK, "%s a minute into the rollout", name)
		assert.Contains(t, strings.Join(state.Warnings, "; "), "rolling update in progress", name)

		objs := rollTo(createdAt(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", mutate), stale),
			"nats-r2", stale)
		state = runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.False(t, *state.Tier2StatefulSetsOK, "%s 16 minutes into the rollout", name)
	}
}

// The bound is 15 minutes without progress, on either side of it.
func TestCheckTier2StatefulSets_StallBoundIsFifteenMinutes(t *testing.T) {
	for age, want := range map[time.Duration]bool{14 * time.Minute: true, 16 * time.Minute: false} {
		at := time.Now().Add(-age)
		objs := rollTo(createdAt(makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"}), at),
			"nats-r2", at)
		state := runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, age)
		assert.Equal(t, want, *state.Tier2StatefulSetsOK, "no progress for %s", age)
	}
}

// podNamed returns the pod called name in objs.
func podNamed(objs []runtime.Object, name string) *corev1.Pod {
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok && p.Name == name {
			return p
		}
	}
	return nil
}

// onRevision puts pod name in objs on revision rev, Ready or not, created at
// created.
func onRevision(objs []runtime.Object, name, rev string, ready bool, created time.Time) []runtime.Object {
	p := podNamed(objs, name)
	p.Labels[appsv1.ControllerRevisionHashLabelKey] = rev
	p.CreationTimestamp = metav1.NewTime(created)
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}
	return objs
}

// A rollout is dated by its update revision, and since then by the pods it
// replaces. A write to the spec that starts no rollout does not restart the
// bound: helm re-dates its entry on a chart-version-only upgrade, and so does
// a revisionHistoryLimit edit, as do the controller's status updates, an
// autoscaler's scale and a label change.
func TestCheckTier2StatefulSets_SpecWritesDoNotRestartTheBound(t *testing.T) {
	longAgo, recent := installedAt, time.Now().Add(-time.Minute)
	wedged := func(writes ...metav1.ManagedFieldsEntry) []runtime.Object {
		objs := rollTo(createdAt(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", nil), longAgo),
			"nats-r2", longAgo)
		sts := objs[0].(*appsv1.StatefulSet)
		sts.ManagedFields = append(sts.ManagedFields, writes...)
		return objs
	}
	for name, objs := range map[string][]runtime.Object{
		"chart-version-only upgrade": wedged(specWrite(installManager, recent)),
		"revisionHistoryLimit edit": wedged(managedFields("kubectl-edit", "",
			`{"f:spec":{"f:revisionHistoryLimit":{}}}`, recent)),
		"status, scale and label writes": wedged(statusWrite(recent),
			managedFields("hpa", "scale", `{"f:spec":{"f:replicas":{}}}`, recent),
			managedFields("kubectl-label", "", `{"f:metadata":{"f:labels":{"f:team":{}}}}`, recent)),
	} {
		state, log := runTier2Logged(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.False(t, *state.Tier2StatefulSetsOK, name)
		assert.Contains(t, log, "rolling update is not progressing: no progress for", name)
	}

	// With the update revision unreadable, the spec write is all that dates
	// the rollout.
	state := runTier2(withoutRevision(wedged(specWrite(installManager, recent)), "nats-r2"))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
}

// A rollback reuses a ControllerRevision created long ago, and lowering a
// partition creates none, so the pods they replace date them: a replacement
// on the update revision created, or a pod deleted.
func TestCheckTier2StatefulSets_RollbackAndLoweredPartitionAreDatedByTheirPods(t *testing.T) {
	longAgo, recent := installedAt, time.Now().Add(-time.Minute)
	rollback := func(replacementCreated time.Time) []runtime.Object {
		objs := rollTo(createdAt(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r0", nil), longAgo),
			"nats-r0", longAgo)
		podNamed(objs, "nats-2").CreationTimestamp = metav1.NewTime(replacementCreated)
		return objs
	}
	state := runTier2(rollback(recent))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "a rollback whose first replacement was created a minute ago")
	state = runTier2(rollback(longAgo))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "a rollback whose replacement has been down since long ago")

	terminating := rollback(longAgo)
	deleted := metav1.NewTime(recent)
	podNamed(terminating, "nats-2").DeletionTimestamp = &deleted
	state = runTier2(terminating)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "a pod the rollback deleted a minute ago")

	// A rollout staged at partition 2 updated nats-2 long ago; lowering the
	// partition replaced nats-1.
	lowered := func(replaced time.Time) []runtime.Object {
		objs := rollTo(createdAt(makeQuorumSTS("nats", "nats-system", 3, 2,
			[]string{"node-1", "node-2", "node-3"}), longAgo), "nats-r2", longAgo)
		objs = onRevision(objs, "nats-2", "nats-r2", true, longAgo)
		objs = onRevision(objs, "nats-1", "nats-r2", false, replaced)
		partition := int32(0)
		objs[0].(*appsv1.StatefulSet).Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition},
		}
		return objs
	}
	state = runTier2(lowered(recent))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "a partition lowered a minute ago")
	state = runTier2(lowered(longAgo))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// A pod created anew is progress only when it is the rollout's newest step.
// A node drain or a failure recreating an old-revision pod, or an already
// updated pod above the newest step, does not restart the bound of a
// rollout whose newest step has been down since long ago.
func TestCheckTier2StatefulSets_RecreatedPodsAreNotProgress(t *testing.T) {
	longAgo, recent := installedAt, time.Now().Add(-time.Minute)
	wedged := func() []runtime.Object {
		return rollTo(createdAt(makeQuorumSTS("nats", "nats-system", 3, 2,
			[]string{"node-1", "node-2", "node-3"}), longAgo), "nats-r2", longAgo)
	}
	for name, objs := range map[string][]runtime.Object{
		"old-revision pod recreated": onRevision(onRevision(wedged(), "nats-2", "nats-r2", false, longAgo),
			"nats-0", "nats-r1", true, recent),
		"updated pod recreated above the newest step": onRevision(onRevision(wedged(),
			"nats-2", "nats-r2", true, recent), "nats-1", "nats-r2", false, longAgo),
	} {
		state := runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.False(t, *state.Tier2StatefulSetsOK, name)
	}

	state := runTier2(onRevision(onRevision(wedged(), "nats-2", "nats-r2", true, longAgo),
		"nats-1", "nats-r2", false, recent))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "the newest step was created a minute ago")
}

// The partition is decided by ordinal. Old-revision pods below it are held
// there by design, and are judged only when down; a down pod at or above it is
// one the rollout replaces next. A pod already updated is not held back,
// whatever its ordinal.
func TestCheckTier2StatefulSets_PartitionIsDecidedByOrdinal(t *testing.T) {
	withPartition := func(objs []runtime.Object, p int32) []runtime.Object {
		objs[0].(*appsv1.StatefulSet).Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type:          appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &p},
		}
		return objs
	}
	// nats-0 and nats-1 are Ready on the old revision; nats-0 has restarted
	// before, which says nothing about a Ready pod.
	oldDown := func(p int32) []runtime.Object {
		objs := withPartition(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r1", nil), p)
		objs[2].(*corev1.Pod).Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "nats", RestartCount: 7}}
		return objs
	}

	state := runTier2(oldDown(1))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "nats-2 is above partition 1, so the rollout replaces it next")

	state = runTier2(oldDown(3))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK, "nats-2 is below partition 3, so the rollout never replaces it")

	state = runTier2(withPartition(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", nil), 3))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK, "an updated pod below the partition is still starting")
}

func TestPodOrdinal(t *testing.T) {
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "nats"}}
	pod := func(name string, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	assert.Equal(t, 1, podOrdinal(sts, pod("nats-x", map[string]string{appsv1.PodIndexLabel: "1"})),
		"the pod-index label is read first")
	assert.Equal(t, 2, podOrdinal(sts, pod("nats-2", nil)), "the name gives it when the label is absent")
	assert.Equal(t, -1, podOrdinal(sts, pod("other-2", nil)))
	assert.Equal(t, -1, podOrdinal(sts, pod("nats-2", map[string]string{appsv1.PodIndexLabel: "two"})))
}

// Only the StatefulSet's own pods are evidence. A selector match it does not
// own, such as a failed nats-box Job pod, says nothing about its rollout.
func TestCheckTier2StatefulSets_UnownedPodIsNotRolloutEvidence(t *testing.T) {
	objs := rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", nil)
	objs = append(objs, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nats-box", Namespace: "nats-system", CreationTimestamp: metav1.Now(),
			Labels: map[string]string{"app": "nats"}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed},
	})
	state := runTier2(objs)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
}

// The rollout warning is the CLI's cue to keep waiting, so it is given only for
// a rollout this run tolerated, never beside a finding that fails it.
func TestCheckTier2StatefulSets_WarnsOnlyAboutToleratedRollouts(t *testing.T) {
	stale := time.Now().Add(-2 * time.Hour)
	twoDown := rollTo(makeQuorumSTS("nats", "nats-system", 3, 1, []string{"node-1"}), "nats-r2", time.Now())
	for name, objs := range map[string][]runtime.Object{
		"no progress": rollTo(createdAt(makeQuorumSTS("nats", "nats-system", 3, 2,
			[]string{"node-1", "node-2"}), stale), "nats-r2", stale),
		"two pods down":    twoDown,
		"co-located peers": rollingNATSWithDownPod([]string{"node-1", "node-1"}, "nats-r2", nil),
		"co-located, at 3 of 3": rollTo(makeQuorumSTS("nats", "nats-system", 3, 3,
			[]string{"node-1", "node-1", "node-2"}), "nats-r2", time.Now()),
	} {
		state, log := runTier2Logged(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.False(t, *state.Tier2StatefulSetsOK, name)
		for _, marker := range cliRolloutMarkers {
			assert.NotContains(t, strings.Join(state.Warnings, "; "), marker, name)
			assert.NotContains(t, log, marker, name)
		}
	}

	state := runTier2(rollingNATSWithDownPod([]string{"node-1", "node-2"}, "nats-r2", nil))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "nats-system/nats: rolling update in progress (ready: 2/3)")
}

// -- Tier-2 HA inference --

// HA is read from the rendered placement: any pod anti-affinity, preferred or
// required (mode enforced), or a hostname topology spread asks for distinct
// nodes. A zone-only spread does not, and neither does no placement at all.
func TestCheckTier2StatefulSets_HAIsReadFromRenderedPlacement(t *testing.T) {
	term := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cassandra"}},
		TopologyKey:   corev1.LabelHostname,
	}
	spread := func(key string) []corev1.TopologySpreadConstraint {
		return []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: key,
			WhenUnsatisfiable: corev1.ScheduleAnyway, LabelSelector: term.LabelSelector}}
	}
	cases := map[string]struct {
		affinity *corev1.Affinity
		spread   []corev1.TopologySpreadConstraint
		ha       bool
	}{
		"preferred anti-affinity": {affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100, PodAffinityTerm: term}}}}, ha: true},
		"required anti-affinity": {affinity: &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{term}}}, ha: true},
		"hostname spread":  {spread: spread(corev1.LabelHostname), ha: true},
		"zone spread only": {spread: spread(corev1.LabelTopologyZone)},
		"no placement":     {},
	}
	for name, tc := range cases {
		objs := makeQuorumSTS("cassandra", "cassandra-system", 1, 1, []string{"node-1"})
		pod := &objs[0].(*appsv1.StatefulSet).Spec.Template.Spec
		pod.Affinity, pod.TopologySpreadConstraints = tc.affinity, tc.spread
		assert.Equal(t, tc.ha, spreadsAcrossNodes(objs[0].(*appsv1.StatefulSet)), name)

		state := runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.Equal(t, !tc.ha, *state.Tier2StatefulSetsOK,
			"one replica has lost quorum only when rendered for HA: %s", name)
	}
}

// Only a term that keeps the StatefulSet's own pods off each other's nodes
// reads as HA. Under mode none, global.affinity may carry anti-affinity
// against another app, or an admission policy may inject a term; neither
// makes a single Cassandra a lost quorum or a single-node NATS co-located.
func TestCheckTier2StatefulSets_ForeignPlacementIsNotHA(t *testing.T) {
	own := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cassandra"}}
	other := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	term := func(sel *metav1.LabelSelector, key string, mutate func(*corev1.PodAffinityTerm)) *corev1.Affinity {
		tm := corev1.PodAffinityTerm{LabelSelector: sel, TopologyKey: key}
		if mutate != nil {
			mutate(&tm)
		}
		return &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{tm}}}
	}
	inNamespaces := func(names ...string) func(*corev1.PodAffinityTerm) {
		return func(tm *corev1.PodAffinityTerm) { tm.Namespaces = names }
	}
	byNamespaceName := func(ns string) func(*corev1.PodAffinityTerm) {
		return func(tm *corev1.PodAffinityTerm) {
			tm.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{corev1.LabelMetadataName: ns}}
		}
	}
	cases := map[string]struct {
		affinity *corev1.Affinity
		spread   *metav1.LabelSelector
		ha       bool
	}{
		"another app's pods":         {affinity: term(other, corev1.LabelHostname, nil)},
		"own pods by zone":           {affinity: term(own, corev1.LabelTopologyZone, nil)},
		"no selector":                {affinity: term(nil, corev1.LabelHostname, nil)},
		"own labels, other ns":       {affinity: term(own, corev1.LabelHostname, inNamespaces("api-system"))},
		"own labels, other ns label": {affinity: term(own, corev1.LabelHostname, byNamespaceName("api-system"))},
		"another app's spread":       {spread: other},
		"own pods":                   {affinity: term(own, corev1.LabelHostname, nil), ha: true},
		"every pod":                  {affinity: term(&metav1.LabelSelector{}, corev1.LabelHostname, nil), ha: true},
		"own ns listed": {affinity: term(own, corev1.LabelHostname,
			inNamespaces("api-system", "cassandra-system")), ha: true},
		"own ns by name label": {affinity: term(own, corev1.LabelHostname,
			byNamespaceName("cassandra-system")), ha: true},
		"own spread": {spread: own, ha: true},
	}
	for name, tc := range cases {
		objs := makeQuorumSTS("cassandra", "cassandra-system", 1, 1, []string{"node-1"})
		sts := objs[0].(*appsv1.StatefulSet)
		sts.Spec.Template.Spec.Affinity = tc.affinity
		if tc.spread != nil {
			sts.Spec.Template.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
				MaxSkew: 1, TopologyKey: corev1.LabelHostname, WhenUnsatisfiable: corev1.ScheduleAnyway,
				LabelSelector: tc.spread}}
		}
		assert.Equal(t, tc.ha, spreadsAcrossNodes(sts), name)
		state := runTier2(objs)
		require.NotNil(t, state.Tier2StatefulSetsOK, name)
		assert.Equal(t, !tc.ha, *state.Tier2StatefulSetsOK, "one Ready replica: %s", name)
	}

	// A single-node mode none NATS at 3/3 is not judged on placement under a
	// foreign term.
	objs := makeQuorumSTS("nats", "nats-system", 3, 3, []string{"node-1", "node-1", "node-1"})
	objs[0].(*appsv1.StatefulSet).Spec.Template.Spec.Affinity = term(other, corev1.LabelHostname, nil)
	state := runTier2(objs)
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
	assert.Equal(t, 1, state.Tier2PlacementNotAssessed)
}

// A known component scaled to zero is down whether or not it was rendered
// for HA.
func TestCheckTier2StatefulSets_UnspreadKnownComponentAtZeroFails(t *testing.T) {
	state := runTier2(withoutSpread(makeQuorumSTS("nats", "nats-system", 0, 0, nil)))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.False(t, *state.Tier2StatefulSetsOK)
}

// Placement is judged only for a StatefulSet that asks for it, during a
// tolerated rollout as at rest: mode none puts peers on one node by design.
func TestCheckTier2StatefulSets_UnspreadRolloutIsNotJudgedOnPlacement(t *testing.T) {
	state := runTier2(withoutSpread(rollingNATSWithDownPod([]string{"node-1", "node-1"}, "nats-r2", nil)))
	require.NotNil(t, state.Tier2StatefulSetsOK)
	assert.True(t, *state.Tier2StatefulSetsOK)
}

// -- Tier-1 rollout --

// tier1Rolling is a 3-replica Deployment, maxUnavailable 1, mid-rollout at its
// floor of 2 Ready, its spec written a minute ago.
func tier1Rolling(conds ...appsv1.DeploymentCondition) *appsv1.Deployment {
	three := int32(3)
	one := intstr.FromInt32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 2,
			CreationTimestamp: metav1.NewTime(installedAt),
			ManagedFields:     []metav1.ManagedFieldsEntry{specWrite(installManager, time.Now().Add(-time.Minute))}},
		Spec: appsv1.DeploymentSpec{Replicas: &three, Strategy: appsv1.DeploymentStrategy{
			RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &one}}},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2, Replicas: 4, UpdatedReplicas: 1, ReadyReplicas: 2, Conditions: conds,
		},
	}
}

func runTier1Plain(d *appsv1.Deployment) *ValidationState {
	state := &ValidationState{Log: testLog()}
	checkTier1Deployments(context.Background(), fake.NewSimpleClientset(d), nil, state)
	return state
}

// Each Progressing reason the controller sets while a rollout moves earns the
// tolerance.
func TestCheckTier1Deployments_EveryRolloutReasonIsTolerated(t *testing.T) {
	for reason := range rolloutProgressingReasons {
		state := runTier1Plain(tier1Rolling(appsv1.DeploymentCondition{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: reason,
		}))
		require.NotNil(t, state.Tier1DeploymentsOK, reason)
		assert.True(t, *state.Tier1DeploymentsOK, reason)
	}
	assert.ElementsMatch(t, []string{"ReplicaSetUpdated", "NewReplicaSetCreated", "FoundNewReplicaSet"},
		mapKeys(rolloutProgressingReasons))
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A generation the controller has not observed has no deadline of its own, so
// it is tolerated only for the bound from the write that set it. One left
// unobserved for days is a controller that is not running.
func TestCheckTier1Deployments_UnobservedGenerationIsBounded(t *testing.T) {
	for age, want := range map[time.Duration]bool{
		time.Minute: true, 14 * time.Minute: true, 16 * time.Minute: false, 30 * 24 * time.Hour: false,
	} {
		d := tier1Rolling()
		d.Generation = 7
		d.Status.ObservedGeneration = 6
		d.ManagedFields = []metav1.ManagedFieldsEntry{
			specWrite(installManager, time.Now().Add(-age)), statusWrite(time.Now()),
			managedFields("hpa", "scale", `{"f:spec":{"f:replicas":{}}}`, time.Now()),
		}
		state := runTier1Plain(d)
		require.NotNil(t, state.Tier1DeploymentsOK, age)
		assert.Equal(t, want, *state.Tier1DeploymentsOK, "generation unobserved since %s ago", age)
	}

	// With no managedFields, the Deployment's creation is the earliest the
	// rollout can have started.
	created := tier1Rolling()
	created.Generation, created.Status.ObservedGeneration = 2, 1
	created.ManagedFields, created.CreationTimestamp = nil, metav1.NewTime(time.Now().Add(-time.Minute))
	state := runTier1Plain(created)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
}

// A rollout the controller gave up on is judged on readiness, even before it
// observes a newer generation.
func TestCheckTier1Deployments_ExceededDeadlineIsNotARollout(t *testing.T) {
	d := tier1Rolling(appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	})
	d.Generation = 3
	state := runTier1Plain(d)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
}

// At full readiness a rollout past its progress deadline still passes, but a
// failed upgrade is reported, in words the CLI does not read as a rollout to
// wait for.
func TestCheckTier1Deployments_ExceededDeadlineAtFullReadinessWarns(t *testing.T) {
	d := tier1Rolling(appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	})
	d.Status.ReadyReplicas = 3
	state := runTier1Plain(d)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	warnings := strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, "nvcf/api: its last rollout exceeded progressDeadlineSeconds")
	for _, marker := range cliRolloutMarkers {
		assert.NotContains(t, warnings, marker)
	}

	state = runTier1Plain(tier1Rolling())
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "progressDeadlineSeconds",
		"a Deployment that never exceeded its deadline is not reported")
}

// controlledBy is a controller reference to owner, of the given kind.
func controlledBy(kind string, owner metav1.Object) []metav1.OwnerReference {
	controller := true
	return []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: kind, Name: owner.GetName(), UID: owner.GetUID(), Controller: &controller,
	}}
}

// rolloutPod is a pod of owner on revision label rev, created at created.
func rolloutPod(name, ns string, labels map[string]string, owner []metav1.OwnerReference, created time.Time,
	ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels, OwnerReferences: owner,
			CreationTimestamp: metav1.NewTime(created)},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}},
	}
}

// noDeadlineRollout is tier1Rolling with no progress deadline, rolling from
// ReplicaSet api-old to api-new, created at started and written to its spec
// then: two old pods Ready since the install and one updated pod, not yet
// Ready, created at stepped.
func noDeadlineRollout(started, stepped time.Time) []runtime.Object {
	d := tier1Rolling()
	noDeadline := int32(math.MaxInt32)
	d.Spec.ProgressDeadlineSeconds = &noDeadline
	d.UID = "uid-api"
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}
	d.ManagedFields = []metav1.ManagedFieldsEntry{specWrite(installManager, started)}
	d.Status.Replicas = 3
	replicaSet := func(hash, revision string, created time.Time) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "api-" + hash, Namespace: "nvcf", UID: types.UID("uid-api-" + hash),
			Labels:            map[string]string{"app": "api", appsv1.DefaultDeploymentUniqueLabelKey: hash},
			Annotations:       map[string]string{deploymentRevisionAnnotation: revision},
			OwnerReferences:   controlledBy("Deployment", d),
			CreationTimestamp: metav1.NewTime(created),
		}}
	}
	oldRS, newRS := replicaSet("old", "1", installedAt), replicaSet("new", "2", started)
	pod := func(name string, rs *appsv1.ReplicaSet, created time.Time, ready bool) *corev1.Pod {
		return rolloutPod(name, "nvcf", rs.Labels, controlledBy("ReplicaSet", rs), created, ready)
	}
	return []runtime.Object{d, oldRS, newRS, pod("api-old-a", oldRS, installedAt, true),
		pod("api-old-b", oldRS, installedAt, true), pod("api-new-a", newRS, stepped, false)}
}

// A Deployment with no progress deadline is tolerated mid-rollout until the
// bound passes without it moving. Its newest ReplicaSet dates the start, and
// each pod of the new ReplicaSet or deletion since is progress, so a healthy
// rollout longer than the bound passes. A spec write that starts no rollout,
// or an old pod recreated, does not restart the bound.
func TestCheckTier1Deployments_NoDeadlineRolloutIsDatedByItsReplicaSets(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	longAgo, twenty, recent := time.Now().Add(-2*time.Hour), time.Now().Add(-20*time.Minute),
		time.Now().Add(-time.Minute)
	reapplied := noDeadlineRollout(longAgo, longAgo)
	reapplied[0].(*appsv1.Deployment).ManagedFields = []metav1.ManagedFieldsEntry{specWrite(installManager, recent)}
	recreated := noDeadlineRollout(longAgo, longAgo)
	podNamed(recreated, "api-old-b").CreationTimestamp = metav1.NewTime(recent)
	terminating := noDeadlineRollout(longAgo, longAgo)
	deleted := metav1.NewTime(recent)
	podNamed(terminating, "api-old-b").DeletionTimestamp = &deleted
	for name, tc := range map[string]struct {
		objs []runtime.Object
		ok   bool
	}{
		"20 minutes in, an updated pod a minute ago": {objs: noDeadlineRollout(twenty, recent), ok: true},
		"a minute in":                         {objs: noDeadlineRollout(recent, recent), ok: true},
		"an old pod deleted a minute ago":     {objs: terminating, ok: true},
		"no step for 20 minutes":              {objs: noDeadlineRollout(twenty, twenty)},
		"spec re-applied, no rollout started": {objs: reapplied},
		"an old pod recreated":                {objs: recreated},
	} {
		state := runTier1(t, false, routeClient(), tc.objs...)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.Equal(t, tc.ok, *state.Tier1DeploymentsOK, name)
	}

	// The spec write dates the rollout only when the ReplicaSets cannot be
	// read.
	for written, ok := range map[time.Time]bool{recent: true, longAgo: false} {
		objs := noDeadlineRollout(longAgo, longAgo)
		objs[0].(*appsv1.Deployment).ManagedFields = []metav1.ManagedFieldsEntry{specWrite(installManager, written)}
		client := gatewayDiscoveryClient(gatewayAPIGroup + "/v1/httproutes")
		for _, o := range objs {
			require.NoError(t, client.Tracker().Add(o))
		}
		client.PrependReactor("list", "replicasets", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "replicasets"},
				"", fmt.Errorf("denied"))
		})
		state := &ValidationState{Log: testLog()}
		checkTier1Deployments(context.Background(), client, routeClient(), state)
		require.NotNil(t, state.Tier1DeploymentsOK, written)
		assert.Equal(t, ok, *state.Tier1DeploymentsOK, "spec written %s", time.Since(written).Round(time.Minute))
	}
}

// natsProxyRollout is NVCF Gateway gw/nats-gw's DaemonSet proxy on desired
// nodes, rolling to template generation 2, whose ControllerRevision was
// created at started, as was the write to its spec. The first updated of its
// pods are on generation 2, the rest on 1, and the first ready of them are
// Ready, all created at created.
func natsProxyRollout(desired, updated, ready int32, started, created time.Time) []runtime.Object {
	sel := map[string]string{"app": "envoy-nats"}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-nats", Namespace: envoyGatewayNamespace, UID: "uid-envoy-nats",
			Generation: 2, Annotations: map[string]string{appsv1.DeprecatedTemplateGeneration: "2"},
			Labels:        map[string]string{owningGatewayNameLabel: "nats-gw", owningGatewayNamespaceLabel: "gw"},
			ManagedFields: []metav1.ManagedFieldsEntry{specWrite(installManager, started)}},
		Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: sel}},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: desired,
			UpdatedNumberScheduled: updated, NumberReady: ready},
	}
	objs := []runtime.Object{ds, &appsv1.ControllerRevision{ObjectMeta: metav1.ObjectMeta{
		Name: controllerRevisionName(ds.Name, "h2"), Namespace: ds.Namespace,
		CreationTimestamp: metav1.NewTime(started)}, Revision: 2}}
	for i := range desired {
		generation, hash := "1", "h1"
		if i < updated {
			generation, hash = "2", "h2"
		}
		labels := map[string]string{"app": "envoy-nats", daemonSetPodGenerationLabel: generation,
			appsv1.DefaultDaemonSetUniqueLabelKey: hash}
		objs = append(objs, rolloutPod(fmt.Sprintf("envoy-nats-%d", i), ds.Namespace, labels,
			controlledBy("DaemonSet", ds), created, i < ready))
	}
	return objs
}

// A DaemonSet proxy rollout is tolerated only until the bound passes without
// it moving, as a Deployment's is: its update ControllerRevision dates the
// start, and an updated pod created or any pod deleted since is progress. A
// proxy held at 2 of 3 updated for a week, or a one-node proxy at 0 of 1, is
// down, not rolling.
func TestCheckTier1Deployments_DaemonSetProxyRolloutIsBounded(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "gw/nats-gw")
	week, recent := time.Now().Add(-7*24*time.Hour), time.Now().Add(-time.Minute)
	steppedRecently := natsProxyRollout(3, 2, 2, week, week)
	podNamed(steppedRecently, "envoy-nats-1").CreationTimestamp = metav1.NewTime(recent)
	oldRecreated := natsProxyRollout(3, 2, 2, week, week)
	podNamed(oldRecreated, "envoy-nats-2").CreationTimestamp = metav1.NewTime(recent)
	for name, tc := range map[string]struct {
		objs []runtime.Object
		ok   bool
	}{
		"2 of 3 updated, started a minute ago":     {objs: natsProxyRollout(3, 2, 2, recent, recent), ok: true},
		"2 of 3 updated, an updated pod just made": {objs: steppedRecently, ok: true},
		"2 of 3 updated for a week":                {objs: natsProxyRollout(3, 2, 2, week, week)},
		"an old pod recreated":                     {objs: oldRecreated},
		"one node at 0 of 1, spec written recently": {
			objs: natsProxyRollout(1, 0, 0, recent, week), ok: true},
		"one node at 0 of 1 for a week": {objs: natsProxyRollout(1, 0, 0, week, week)},
		"revision unreadable, spec written recently": {
			objs: withoutRevision(natsProxyRollout(3, 2, 2, recent, week), "envoy-nats-h2"), ok: true},
	} {
		gateways := envoyGatewayClient(t, gatewayObject("gw", "nats-gw", "eg"))
		state := runTier1(t, false, gateways, tc.objs...)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.Equal(t, tc.ok, *state.Tier1DeploymentsOK, name)
	}

	// The revision, when it can be read, dates the start: a later write to
	// the spec does not restart the bound.
	objs := natsProxyRollout(3, 2, 2, week, week)
	objs[0].(*appsv1.DaemonSet).ManagedFields = []metav1.ManagedFieldsEntry{specWrite(installManager, recent)}
	state := runTier1(t, false, envoyGatewayClient(t, gatewayObject("gw", "nats-gw", "eg")), objs...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
}

// -- Tier-1 proxies and Gateway coverage --

// envoyProxyAt is an Envoy proxy Deployment in ns with the given labels.
func envoyProxyAt(ns, name string, labels map[string]string, replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1, Labels: labels},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: replicas, ReadyReplicas: ready},
	}
}

func nvcfAPI() *appsv1.Deployment {
	return envoyProxyAt("nvcf", "api", nil, 2, 2)
}

// Merged-gateways mode labels its proxy by GatewayClass only. After install
// that proxy, Ready, covers every NVCF Gateway of its class.
func TestCheckTier1Deployments_MergedProxyCoversItsClassAfterInstall(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	merged := envoyProxyAt(envoyGatewayNamespace, "envoy-merged",
		map[string]string{owningGatewayClassLabel: "eg"}, 2, 2)
	state := runTier1(t, true, envoyGatewayClient(t, gatewayObject("nvcf", "shared-gw", "eg")), merged, nvcfAPI())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
}

// After install the Envoy Gateway controller is judged like an NVCF
// Deployment, wherever it runs: dead, it leaves route changes and every new
// or restarted proxy unprogrammed. Before install, and when another
// implementation runs every NVCF Gateway, the Envoy row alone reports it.
func TestCheckTier1Deployments_PostInstallJudgesTheEnvoyController(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	labels := map[string]string{owningGatewayNameLabel: "nvcf-gw", owningGatewayNamespaceLabel: "nvcf"}
	deployments := appsv1.SchemeGroupVersion.WithResource("deployments")
	// cluster holds the controller in ns, Ready or not, in place of the
	// fixture's Ready one, beside a Ready NVCF proxy and service.
	cluster := func(ns string, ready int32) *fake.Clientset {
		client := gatewayDiscoveryClient(gatewayAPIGroup+"/v1/httproutes", gatewayAPIGroup+"/v1/gateways",
			gatewayAPIGroup+"/v1/gatewayclasses")
		require.NoError(t, client.Tracker().Delete(deployments, envoyGatewayNamespace, "envoy-gateway"))
		controller := envoyController(ns)
		controller.Status.ReadyReplicas = ready
		for _, o := range []runtime.Object{controller, envoyProxyAt(ns, "envoy-nvcf", labels, 2, 2), nvcfAPI()} {
			require.NoError(t, client.Tracker().Add(o))
		}
		return client
	}
	gateways := func(class string) dynamic.Interface {
		return envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", class))
	}
	for name, tc := range map[string]struct {
		postInstall bool
		class, ns   string
		ready       int32
		ok          bool
	}{
		"down after install":                 {postInstall: true, class: "eg", ns: envoyGatewayNamespace},
		"down after install, found by label": {postInstall: true, class: "eg", ns: "custom-envoy-system"},
		"Ready after install": {
			postInstall: true, class: "eg", ns: envoyGatewayNamespace, ready: 1, ok: true},
		"down before install":             {class: "eg", ns: envoyGatewayNamespace, ok: true},
		"down, NVCF Gateway run by istio": {postInstall: true, class: "istio", ns: envoyGatewayNamespace, ok: true},
	} {
		state := &ValidationState{Log: testLog(), PostInstall: tc.postInstall}
		checkTier1Deployments(context.Background(), cluster(tc.ns, tc.ready), gateways(tc.class), state)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.Equal(t, tc.ok, *state.Tier1DeploymentsOK, name)
	}

	// A controller that cannot be read after install leaves the row unknown.
	client := cluster(envoyGatewayNamespace, 1)
	client.PrependReactor("list", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.(ktesting.ListAction).GetListRestrictions().Labels.String() == envoyGatewayControllerSelector {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
		}
		return false, nil, nil
	})
	state := &ValidationState{Log: testLog(), PostInstall: true}
	checkTier1Deployments(context.Background(), client, gateways("eg"), state)
	assert.Nil(t, state.Tier1DeploymentsOK)
	assert.Contains(t, state.Warnings, tier1ControllerUnknown)
}

// A proxy Deployment with no Ready pod does not cover its Gateway, wherever
// the controller runs, including a namespace Tier-1 does not otherwise scan.
func TestCheckTier1Deployments_ProxyDeploymentWithoutReadyPodIsAGap(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	labels := map[string]string{owningGatewayNameLabel: "nvcf-gw", owningGatewayNamespaceLabel: "nvcf"}
	gateways := func() dynamic.Interface {
		return envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg"))
	}
	controller := func() *appsv1.Deployment { return envoyController("custom-envoy-system") }
	state := runTier1(t, true, gateways(), controller(),
		envoyProxyAt("custom-envoy-system", "envoy-nvcf", labels, 2, 0), nvcfAPI())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)

	state = runTier1(t, true, gateways(), controller(),
		envoyProxyAt("custom-envoy-system", "envoy-nvcf", labels, 2, 2), nvcfAPI())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
}

// A missing proxy is a finding even when every Deployment is mid-rollout, so
// the tolerated rollouts cannot carry the row to a pass.
func TestCheckTier1Deployments_GatewayGapWinsOverRollouts(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	state := runTier1(t, true, envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw", "eg")),
		tier1Rolling(rolloutProgressing()))
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
}

// NVCF's own proxy scaled to zero fails like any NVCF service; the shared
// namespace leniency is for other installs' controllers only.
func TestCheckTier1Deployments_NVCFProxyAtZeroFails(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/shared-gw")
	labels := map[string]string{owningGatewayNameLabel: "shared-gw", owningGatewayNamespaceLabel: "nvcf"}
	state := runTier1(t, false, routeClient(), envoyProxyAt(envoyGatewayNamespace, "envoy-nvcf", labels, 0, 0))
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
}

// The shared-namespace rule follows a relocated Envoy Gateway: a workload
// there that no stack release installed is not judged.
func TestCheckTier1Deployments_RelocatedEnvoyNamespaceIsShared(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "custom-envoy")
	t.Setenv(nvcfGatewayNamesEnv, "")
	state := runTier1(t, false, routeClient(), envoyProxyAt("custom-envoy", "parked", nil, 0, 0), nvcfAPI())
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	assert.NotContains(t, strings.Join(state.Warnings, "; "), "custom-envoy/parked")
}

// A Gateway namespace Tier-1 scans only for proxies holds other workloads that
// are not NVCF's to judge.
func TestCheckTier1Deployments_GatewayOnlyNamespaceIgnoresNonProxies(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "team-gw/nvcf-gw")
	labels := map[string]string{owningGatewayNameLabel: "nvcf-gw", owningGatewayNamespaceLabel: "team-gw"}
	state := runTier1(t, false, routeClient(),
		envoyProxyAt("team-gw", "envoy-nvcf", labels, 2, 2), envoyProxyAt("team-gw", "team-app", nil, 2, 0))
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
}

// unreadableRoutes fails every route LIST, so no proxy's owner can be decided.
func unreadableRoutes() dynamic.Interface {
	routes := routeClient()
	routes.PrependReactor("list", "*", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	return routes
}

// A proxy of undecided owner scaled to zero serves nothing, so it leaves the
// row undecided rather than passing as Ready.
func TestCheckTier1Deployments_UndecidedProxyAtZeroIsUnknown(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	labels := map[string]string{owningGatewayNameLabel: "gw", owningGatewayNamespaceLabel: "nvcf"}
	state := runTier1(t, false, unreadableRoutes(), envoyProxyAt(envoyGatewayNamespace, "envoy", labels, 0, 0))
	assert.Nil(t, state.Tier1DeploymentsOK)
}

// A Ready proxy of undecided owner was assessed, so the run does not report
// the control-plane namespaces as empty.
func TestCheckTier1Deployments_ReadyUndecidedProxyIsAssessed(t *testing.T) {
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "")
	labels := map[string]string{owningGatewayNameLabel: "gw", owningGatewayNamespaceLabel: "nvcf"}
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	client := gatewayDiscoveryClient(gatewayAPIGroup+"/v1/httproutes", gatewayAPIGroup+"/v1/gateways")
	_, err := client.AppsV1().Deployments(envoyGatewayNamespace).Create(context.Background(),
		envoyProxyAt(envoyGatewayNamespace, "envoy", labels, 2, 2), metav1.CreateOptions{})
	require.NoError(t, err)
	state := &ValidationState{Log: logrus.NewEntry(l)}
	checkTier1Deployments(context.Background(), client, unreadableRoutes(), state)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, buf.String(), "All 1 assessed Deployment(s)")
	assert.NotContains(t, buf.String(), "pre-install state")
}

// The routes-missing warning blames the routes release only when the cluster
// serves route kinds at all. Without them, the CRD check reports the cause.
func TestResolveGatewayOwnership_RoutesMissingNeedsServedRouteKinds(t *testing.T) {
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	noRouteKinds := gatewayDiscoveryClient(gatewayAPIGroup+"/v1/gateways", gatewayAPIGroup+"/v1/gatewayclasses")
	own := resolveGatewayOwnership(context.Background(), noRouteKinds, routeClient())
	assert.False(t, own.routesMissing)

	own = resolveGatewayOwnership(context.Background(), routeDiscoveryClient(), routeClient())
	assert.True(t, own.routesMissing)
}
