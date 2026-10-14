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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

const instanceKey = "app.kubernetes.io/instance"

// topoNode is a Ready node labelled with its hostname and, unless zone is
// empty, its zone.
func topoNode(name, zone string, mutate ...func(*corev1.Node)) *corev1.Node {
	n := makeNode(name, true, 0)
	n.Labels = map[string]string{corev1.LabelHostname: name}
	if zone != "" {
		n.Labels[corev1.LabelTopologyZone] = zone
	}
	for _, m := range mutate {
		m(n)
	}
	return n
}

func taintedWith(key string, effect corev1.TaintEffect) func(*corev1.Node) {
	return func(n *corev1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: key, Value: "true", Effect: effect})
	}
}

func labelledWith(key, value string) func(*corev1.Node) {
	return func(n *corev1.Node) { n.Labels[key] = value }
}

// zonedNodes returns Ready nodes node-1..node-n placed round-robin in zones,
// unlabelled when no zone is given.
func zonedNodes(n int, zones ...string) []runtime.Object {
	objs := make([]runtime.Object, n)
	for i := range objs {
		zone := ""
		if len(zones) > 0 {
			zone = zones[i%len(zones)]
		}
		objs[i] = topoNode(fmt.Sprintf("node-%d", i+1), zone)
	}
	return objs
}

func threeZones(n int) []runtime.Object { return zonedNodes(n, "zone-a", "zone-b", "zone-c") }

// quorumSet is a quorum StatefulSet whose pod template carries labels, as its
// release renders it, and spec.
func quorumSet(ns, name string, podLabels map[string]string, spec corev1.PodSpec) *appsv1.StatefulSet {
	three := int32(3)
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: podLabels},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &three,
			Selector: &metav1.LabelSelector{MatchLabels: podLabels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: spec},
		},
	}
}

// The stack's three quorum StatefulSets, named and labelled as their releases
// render them.
func natsSet(spec corev1.PodSpec) *appsv1.StatefulSet {
	return quorumSet("nats-system", "nats", map[string]string{
		instanceKey: "nats", "app.kubernetes.io/component": "nats", "app.kubernetes.io/name": "nats",
	}, spec)
}

func openBaoSet(spec corev1.PodSpec) *appsv1.StatefulSet {
	return quorumSet("vault-system", "openbao-server", map[string]string{
		instanceKey: "openbao-server", "component": "server", "app.kubernetes.io/name": "openbao",
	}, spec)
}

func cassandraSet(spec corev1.PodSpec) *appsv1.StatefulSet {
	return quorumSet("cassandra-system", "cassandra", map[string]string{
		instanceKey: "cassandra", "app.kubernetes.io/name": "cassandra",
	}, spec)
}

func installedQuorum(spec corev1.PodSpec) []runtime.Object {
	return []runtime.Object{natsSet(spec), openBaoSet(spec), cassandraSet(spec)}
}

func tier1Deployment(ns, name, instance string, replicas int32, spec corev1.PodSpec) *appsv1.Deployment {
	podLabels := map[string]string{instanceKey: instance, "app.kubernetes.io/name": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: podLabels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: podLabels}, Spec: spec},
		},
	}
}

func bindingClass(name string, mode *storagev1.VolumeBindingMode, isDefault bool) *storagev1.StorageClass {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: name}, VolumeBindingMode: mode}
	if isDefault {
		sc.Annotations = map[string]string{defaultClassAnnotation: annotationTrue}
	}
	return sc
}

func bindingMode(m storagev1.VolumeBindingMode) *storagev1.VolumeBindingMode { return &m }

// delayedDefault is a default StorageClass that binds WaitForFirstConsumer.
func delayedDefault() *storagev1.StorageClass {
	return bindingClass("standard", bindingMode(storagev1.VolumeBindingWaitForFirstConsumer), true)
}

func selectorSpec(key, value string) corev1.PodSpec {
	return corev1.PodSpec{NodeSelector: map[string]string{key: value}}
}

func runTopology(t *testing.T, mode HAMode, postInstall bool, objs ...runtime.Object) *ValidationState {
	t.Helper()
	return runTopologyWith(t, fake.NewSimpleClientset(objs...), mode, postInstall)
}

func runTopologyWith(t *testing.T, client *fake.Clientset, mode HAMode, postInstall bool) *ValidationState {
	t.Helper()
	state := &ValidationState{Log: testLog(), Role: RoleControlPlane, HAMode: mode, PostInstall: postInstall}
	checkHATopology(context.Background(), client, state)
	return state
}

// topologyWarnings returns the warnings the row added, without their prefix.
func topologyWarnings(s *ValidationState) []string {
	var out []string
	for _, w := range s.Warnings {
		if rest, ok := strings.CutPrefix(w, haTopologyLabel+": "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// requireRow requires the row's result: nil for unknown.
func requireRow(t *testing.T, s *ValidationState, want *bool) {
	t.Helper()
	if want == nil {
		require.Nil(t, s.HATopologyOK, "warnings: %q", s.Warnings)
		return
	}
	require.NotNil(t, s.HATopologyOK, "warnings: %q", s.Warnings)
	require.Equal(t, *want, *s.HATopologyOK, "warnings: %q", s.Warnings)
}

var (
	rowPass = cpRow(true)
	rowFail = cpRow(false)
)

// byMode runs fn under preferred and enforced.
func byMode(t *testing.T, fn func(t *testing.T, mode HAMode)) {
	for _, mode := range []HAMode{HAModePreferred, HAModeEnforced} {
		t.Run(string(mode), func(t *testing.T) { fn(t, mode) })
	}
}

// Without an HA mode nothing is asked of the topology: the check reads
// nothing, the row does not apply, and it neither blocks the verdict nor
// turns a clean run into a pass with warnings.
func TestCheckHATopology_NotApplicableWithoutAnHAMode(t *testing.T) {
	for mode, row := range map[HAMode]string{
		"":         haTopologyLabel + ": Not Assessed (HA mode not supplied)",
		HAModeNone: haTopologyLabel + ": Not Applicable (HA mode none)",
	} {
		t.Run(string(mode), func(t *testing.T) {
			client := fake.NewSimpleClientset(zonedNodes(1)...)
			s := runTopologyWith(t, client, mode, true)
			assert.Nil(t, s.HATopologyOK)
			assert.Empty(t, s.Warnings)
			assert.Empty(t, client.Actions(), "the row reads nothing it will not judge")

			assert.True(t, haTopologyRow(s).NotApplicable)
			assert.Equal(t, row, haTopologyRow(s).NAMsg)

			log, buf := bufferLog()
			state := healthyControlPlaneState()
			state.Log, state.HAMode = log, mode
			require.NoError(t, printSummary(state))
			assert.Contains(t, buf.String(), row)
			assert.NotContains(t, buf.String(), VerdictReadyWithWarnings,
				"a row that does not apply is no warning on an otherwise clean run")
			assert.Contains(t, buf.String(), VerdictLinePrefix+VerdictReady)
		})
	}
}

func TestCheckHATopology_HealthyInstallPasses(t *testing.T) {
	byMode(t, func(t *testing.T, mode HAMode) {
		objs := append(threeZones(6), installedQuorum(corev1.PodSpec{})...)
		objs = append(objs, tier1Deployment("nvcf", "api", "api", 2, corev1.PodSpec{}), delayedDefault())
		s := runTopology(t, mode, true, objs...)
		requireRow(t, s, rowPass)
		assert.Empty(t, s.Warnings)
		assert.Empty(t, s.Recommendations)
		assert.Equal(t, haTopologyLabel+": Meets HA Mode "+string(mode), haTopologyRow(s).PassMsg)
	})
}

// A three-member quorum needs three nodes in either HA mode. Fewer fails, and
// the zone and per-service findings that follow from it are not repeated.
func TestCheckHATopology_FewerThanThreeNodesFailsInBothModes(t *testing.T) {
	byMode(t, func(t *testing.T, mode HAMode) {
		s := runTopology(t, mode, false, append(zonedNodes(2, "zone-a", "zone-b"), delayedDefault())...)
		requireRow(t, s, rowFail)
		assert.Empty(t, topologyWarnings(s), "only the node count is reported")
		assert.Contains(t, s.Recommendations, haNodesAdvice)
		assert.NotContains(t, s.Recommendations, haZonesAdvice)
		assert.Equal(t, haTopologyLabel+": Below HA Mode "+string(mode), haTopologyRow(s).FailMsg)
	})
}

// A NotReady, cordoned, fenced or non-Linux node takes no control-plane pod.
func TestCheckHATopology_UnusableNodesDoNotCount(t *testing.T) {
	deleting := metav1.NewTime(time.Now())
	for name, unusable := range map[string]*corev1.Node{
		"NotReady": func() *corev1.Node { n := topoNode("node-3", "zone-c"); n.Status.Conditions = nil; return n }(),
		"cordoned": topoNode("node-3", "zone-c", func(n *corev1.Node) { n.Spec.Unschedulable = true }),
		"being deleted": topoNode("node-3", "zone-c", func(n *corev1.Node) {
			n.DeletionTimestamp, n.Finalizers = &deleting, []string{"x"}
		}),
		"fenced":  topoNode("node-3", "zone-c", taintedWith(corev1.TaintNodeUnreachable, corev1.TaintEffectNoExecute)),
		"Windows": topoNode("node-3", "zone-c", labelledWith(corev1.LabelOSStable, "windows")),
	} {
		t.Run(name, func(t *testing.T) {
			objs := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"), unusable,
				delayedDefault()}
			requireRow(t, runTopology(t, HAModePreferred, false, objs...), rowFail)
		})
	}
}

// runTopologyLogged runs the row and returns its state and log.
func runTopologyLogged(t *testing.T, mode HAMode, postInstall bool, objs ...runtime.Object) (*ValidationState,
	string) {
	t.Helper()
	log, buf := bufferLog()
	state := &ValidationState{Log: log, Role: RoleControlPlane, HAMode: mode, PostInstall: postInstall}
	checkHATopology(context.Background(), fake.NewSimpleClientset(objs...), state)
	return state, buf.String()
}

// Before install no pod template says which taints the quorum services will
// tolerate, so only nodes without scheduling taints count: two of them fail
// in either mode. A node with any other scheduling taint is named as one the
// tolerations given at install may admit; one tainted for the Kubernetes
// control plane, which no release tolerates by default, is not.
func TestCheckHATopology_TaintsBeforeInstall(t *testing.T) {
	cases := map[string]struct {
		taint corev1.Taint
		want  *bool
		named bool
	}{
		"pool taint": {corev1.Taint{Key: "nvcf.nvidia.com/workload", Value: "cassandra",
			Effect: corev1.TaintEffectNoSchedule}, rowFail, true},
		"NoExecute pool taint": {corev1.Taint{Key: "dedicated", Effect: corev1.TaintEffectNoExecute}, rowFail, true},
		"control-plane taint": {corev1.Taint{Key: "node-role.kubernetes.io/control-plane",
			Effect: corev1.TaintEffectNoSchedule}, rowFail, false},
		"PreferNoSchedule": {corev1.Taint{Key: "dedicated", Effect: corev1.TaintEffectPreferNoSchedule}, rowPass, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			byMode(t, func(t *testing.T, mode HAMode) {
				tainted := topoNode("node-3", "zone-c", func(n *corev1.Node) { n.Spec.Taints = []corev1.Taint{tc.taint} })
				s, log := runTopologyLogged(t, mode, false, topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"),
					tainted, delayedDefault())
				requireRow(t, s, tc.want)
				assert.Empty(t, topologyWarnings(s))
				if tc.want == rowPass {
					return
				}
				assert.Contains(t, log, "quorum services: 2 node(s) can run them (node-1, node-2); mode "+string(mode)+
					" requires at least 3")
				if tc.named {
					assert.Contains(t, log, "Not counted: 1 node(s) with NoSchedule or NoExecute taints (node-3), "+
						"which tolerations the services are given at install (global.tolerations) may admit")
				} else {
					assert.NotContains(t, log, "Not counted")
				}
			})
		})
	}
}

// Zones are counted on the untainted nodes too: a zone reached only through a
// tainted node does not count before install, and the finding names it.
func TestCheckHATopology_ZoneOnlyOnATaintedNodeDoesNotCount(t *testing.T) {
	objs := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"), topoNode("node-3", "zone-b"),
		topoNode("node-4", "zone-c", taintedWith("dedicated", corev1.TaintEffectNoSchedule)), delayedDefault()}
	s, _ := runTopologyLogged(t, HAModePreferred, false, objs...)
	requireRow(t, s, rowPass)
	warnings := topologyWarnings(s)
	require.Len(t, warnings, 1)
	for _, want := range []string{"span 2 zone(s)", "2+1", "not AZ-HA", "Not counted: 1 node(s)", "node-4"} {
		assert.Contains(t, warnings[0], want)
	}

	s, log := runTopologyLogged(t, HAModeEnforced, false, objs...)
	requireRow(t, s, rowFail)
	assert.Empty(t, topologyWarnings(s))
	assert.Contains(t, log, "mode enforced requires at least 3 zones")
}

// After install the live pod templates say which taints the quorum services
// tolerate, so a tainted node counts only for a service that tolerates it.
func TestCheckHATopology_TaintsAfterInstall(t *testing.T) {
	poolTaint := taintedWith("dedicated", corev1.TaintEffectNoSchedule)
	nodes := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"),
		topoNode("node-3", "zone-c", poolTaint), delayedDefault()}
	tolerating := corev1.PodSpec{Tolerations: []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpExists}}}
	wrongEffect := corev1.PodSpec{Tolerations: []corev1.Toleration{{Key: "dedicated",
		Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}}}

	byMode(t, func(t *testing.T, mode HAMode) {
		s := runTopology(t, mode, true, append(installedQuorum(tolerating), nodes...)...)
		requireRow(t, s, rowPass)
		assert.Empty(t, s.Warnings)

		for name, spec := range map[string]corev1.PodSpec{"none": {}, "another effect": wrongEffect} {
			s = runTopology(t, mode, true, append(installedQuorum(spec), nodes...)...)
			requireRow(t, s, rowFail)
			assert.Empty(t, topologyWarnings(s), name)
		}
	})

	// A numeric toleration is in a live template only where the apiserver
	// accepts it, so it is read as the scheduler reads it.
	numeric := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"),
		topoNode("node-3", "zone-c", func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "gen", Value: "5", Effect: corev1.TaintEffectNoSchedule}}
		}), delayedDefault()}
	for value, want := range map[string]*bool{"4": rowPass, "5": rowFail} {
		gt := corev1.PodSpec{Tolerations: []corev1.Toleration{{Key: "gen", Operator: corev1.TolerationOpGt,
			Value: value}}}
		requireRow(t, runTopology(t, HAModeEnforced, true, append(installedQuorum(gt), numeric...)...), want)
	}
}

// Node selectors and required node affinity narrow the nodes a quorum service
// can run on; a node matching neither does not count.
func TestCheckHATopology_NodeSelectorAndAffinity(t *testing.T) {
	pool := func(names ...string) []runtime.Object {
		objs := threeZones(6)
		for _, o := range objs {
			n := o.(*corev1.Node)
			for _, name := range names {
				if n.Name == name {
					n.Labels["pool"] = "tier2"
				}
			}
		}
		return append(objs, delayedDefault())
	}
	affinity := func(term corev1.NodeSelectorTerm) corev1.PodSpec {
		return corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{term},
			},
		}}}
	}
	byMode(t, func(t *testing.T, mode HAMode) {
		for name, tc := range map[string]struct {
			spec corev1.PodSpec
			want *bool
		}{
			"selector on 2 nodes": {selectorSpec("pool", "tier2"), rowFail},
			"affinity In on 2 nodes": {affinity(corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "pool", Operator: corev1.NodeSelectorOpIn, Values: []string{"tier2"}},
			}}), rowFail},
			"affinity on 2 names": {affinity(corev1.NodeSelectorTerm{MatchFields: []corev1.NodeSelectorRequirement{
				{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-1", "node-2"}},
			}}), rowFail},
			"affinity NotIn the pool": {affinity(corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: "pool", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"tier2"}},
			}}), rowPass},
			"empty affinity term": {affinity(corev1.NodeSelectorTerm{}), rowFail},
		} {
			s := runTopology(t, mode, true, append(installedQuorum(tc.spec), pool("node-1", "node-2")...)...)
			requireRow(t, s, tc.want)
			assert.Empty(t, topologyWarnings(s), name)
		}
	})
}

// Fewer than 3 zones under the quorum nodes fails under enforced and warns
// under preferred, saying that a 2+1 split across 2 zones is not AZ-HA.
func TestCheckHATopology_Zones(t *testing.T) {
	for name, tc := range map[string]struct {
		nodes  []runtime.Object
		detail string
	}{
		"2 zones":   {zonedNodes(3, "zone-a", "zone-b"), "span 2 zone(s)"},
		"1 zone":    {zonedNodes(4, "zone-a"), "span 1 zone(s)"},
		"no labels": {zonedNodes(3), "3 node(s) carry no " + corev1.LabelTopologyZone},
	} {
		t.Run(name, func(t *testing.T) {
			objs := append(tc.nodes, delayedDefault())
			s := runTopology(t, HAModePreferred, false, objs...)
			requireRow(t, s, rowPass)
			warnings := topologyWarnings(s)
			require.Len(t, warnings, 1)
			for _, want := range []string{tc.detail, "mode preferred asks for at least 3 zones (best-effort)", "2+1",
				"not AZ-HA"} {
				assert.Contains(t, warnings[0], want)
			}
			assert.Equal(t, haTopologyLabel+": Meets HA Mode preferred (with warnings)", haTopologyRow(s).PassMsg)
			assert.Contains(t, s.Recommendations, haZonesAdvice)

			s = runTopology(t, HAModeEnforced, false, objs...)
			requireRow(t, s, rowFail)
			assert.Empty(t, topologyWarnings(s))
		})
	}
	byMode(t, func(t *testing.T, mode HAMode) {
		s := runTopology(t, mode, false, append(threeZones(3), delayedDefault())...)
		requireRow(t, s, rowPass)
		assert.Empty(t, s.Warnings)
	})
}

// Zones are counted on the nodes the quorum services can use, not on every
// node: a third zone they are kept out of does not count.
func TestCheckHATopology_ZonesCountOnlyUsableNodes(t *testing.T) {
	objs := []runtime.Object{
		topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"), topoNode("node-3", "zone-b"),
		topoNode("node-4", "zone-c", labelledWith("gpu", "true")), delayedDefault(),
	}
	objs = append(objs, installedQuorum(corev1.PodSpec{Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "gpu", Operator: corev1.NodeSelectorOpDoesNotExist}},
		}}},
	}}})...)
	requireRow(t, runTopology(t, HAModeEnforced, true, objs...), rowFail)
}

// Each installed quorum service needs 3 nodes of its own to hold one member
// each, however many nodes the others can use.
func TestCheckHATopology_QuorumServiceCapacity(t *testing.T) {
	nodes := threeZones(6)
	for _, o := range nodes[:2] {
		o.(*corev1.Node).Labels["nvcf.nvidia.com/workload"] = "cassandra"
	}
	objs := append(nodes, natsSet(corev1.PodSpec{}), openBaoSet(corev1.PodSpec{}),
		cassandraSet(selectorSpec("nvcf.nvidia.com/workload", "cassandra")), delayedDefault())

	s := runTopology(t, HAModePreferred, true, objs...)
	requireRow(t, s, rowPass)
	warnings := topologyWarnings(s)
	require.Len(t, warnings, 1)
	for _, want := range []string{"cassandra-system/cassandra", "2 eligible node(s)",
		"mode preferred asks for at least 3 (best-effort)", "nvcf.nvidia.com/workload=cassandra"} {
		assert.Contains(t, warnings[0], want)
	}
	assert.Contains(t, s.Recommendations, haCapacityAdvice)

	s = runTopology(t, HAModeEnforced, true, objs...)
	requireRow(t, s, rowFail)
	assert.Empty(t, topologyWarnings(s))
}

// A Tier-1 Deployment's rollout adds a surge pod, so its pods need one node
// more than its replicas to stay one per node.
func TestCheckHATopology_Tier1SurgeCapacity(t *testing.T) {
	nodes := func() []runtime.Object {
		objs := threeZones(6)
		for _, o := range objs[:3] {
			o.(*corev1.Node).Labels["pool"] = "cp"
		}
		return append(objs, delayedDefault())
	}
	pool := selectorSpec("pool", "cp")
	for name, tc := range map[string]struct {
		deploy *appsv1.Deployment
		short  string
	}{
		"2 replicas, 3 nodes": {tier1Deployment("nvcf", "api", "api", 2, pool), ""},
		"3 replicas, 3 nodes": {tier1Deployment("nvcf", "api", "api", 3, pool),
			"nvcf/api: 3 eligible node(s)"},
		"ESS is in the convention": {tier1Deployment("ess", "ess-api", "ess-api", 3, pool),
			"ess/ess-api: 3 eligible node(s)"},
		"admin-issuer-proxy in api-keys": {tier1Deployment("api-keys", "admin-issuer-proxy",
			"admin-issuer-proxy", 3, pool), "api-keys/admin-issuer-proxy: 3 eligible node(s)"},
		"nats-auth-callout in nats-system": {tier1Deployment("nats-system", "nats-auth-callout-service",
			"nats-auth-callout-service", 3, pool), "nats-system/nats-auth-callout-service: 3 eligible node(s)"},
		"a named singleton is not judged": {tier1Deployment("nvcf", "grpc-proxy", "grpc-proxy", 3, pool), ""},
		"a Deployment outside the table is not judged": {tier1Deployment("nvcf", "vanity-gateway",
			"vanity-gateway", 3, pool), ""},
		"scaled to zero has no rollout": {tier1Deployment("nvcf", "api", "api", 0, pool), ""},
	} {
		t.Run(name, func(t *testing.T) {
			objs := append(nodes(), installedQuorum(corev1.PodSpec{})...)
			objs = append(objs, tc.deploy)
			s := runTopology(t, HAModePreferred, true, objs...)
			requireRow(t, s, rowPass)
			if tc.short == "" {
				assert.Empty(t, s.Warnings)
				return
			}
			warnings := topologyWarnings(s)
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], tc.short)
			assert.Contains(t, warnings[0], "for 3 replica(s); mode preferred asks for 4 (best-effort)")

			requireRow(t, runTopology(t, HAModeEnforced, true, objs...), rowFail)
		})
	}
}

// The quorum services are found by their release's instance label and, where
// other pods share it, the label that sets their own pods apart.
func TestCheckHATopology_FindsTheQuorumByItsLabels(t *testing.T) {
	look := quorumSet("nats-system", "nats-box-like", map[string]string{
		instanceKey: "nats", "app.kubernetes.io/component": "nats-box",
	}, selectorSpec("pool", "none"))
	objs := append(threeZones(3), installedQuorum(corev1.PodSpec{})...)
	objs = append(objs, look, delayedDefault())
	s := runTopology(t, HAModeEnforced, true, objs...)
	requireRow(t, s, rowPass)

	// A relocated OpenBao is found where it runs.
	t.Setenv(openBaoNamespaceEnv, "openbao")
	moved := openBaoSet(selectorSpec("pool", "none"))
	moved.Namespace = "openbao"
	objs = append(threeZones(3), natsSet(corev1.PodSpec{}), cassandraSet(corev1.PodSpec{}), moved, delayedDefault())
	requireRow(t, runTopology(t, HAModeEnforced, true, objs...), rowFail)
}

// A quorum service that runs outside the cluster needs no nodes in it.
func TestCheckHATopology_ExternalQuorumServicesAreNotJudged(t *testing.T) {
	objs := append(zonedNodes(2, "zone-a", "zone-b"), delayedDefault())
	t.Setenv(externalComponentsEnv, "nats,openbao,cassandra")
	s := runTopology(t, HAModeEnforced, true, objs...)
	requireRow(t, s, rowPass)
	assert.Empty(t, s.Warnings)

	t.Setenv(externalComponentsEnv, "cassandra")
	requireRow(t, runTopology(t, HAModeEnforced, true, objs...), rowFail)
}

// The StorageClass the quorum volumes bind to must delay binding until their
// pod is placed: Immediate fails under enforced and warns under preferred.
// An unset mode is the API default, Immediate.
func TestCheckHATopology_StorageBindingMode(t *testing.T) {
	immediate := bindingMode(storagev1.VolumeBindingImmediate)
	delayed := bindingMode(storagev1.VolumeBindingWaitForFirstConsumer)
	// The apiserver gives a claim the newest default, wherever it lists.
	dated := func(name string, mode *storagev1.VolumeBindingMode, age time.Duration) *storagev1.StorageClass {
		sc := bindingClass(name, mode, true)
		sc.CreationTimestamp = metav1.NewTime(time.Now().Add(-age))
		return sc
	}
	newestLast := []runtime.Object{dated("a-old", delayed, time.Hour), dated("b-new", immediate, 0)}
	newestFirst := []runtime.Object{dated("a-new", delayed, 0), dated("b-old", immediate, time.Hour)}
	for name, tc := range map[string]struct {
		named   string
		classes []runtime.Object
		judged  string
	}{
		"Immediate default":   {"", []runtime.Object{bindingClass("standard", immediate, true)}, "standard"},
		"unset is Immediate":  {"", []runtime.Object{bindingClass("standard", nil, true)}, "standard"},
		"the newest default":  {"", newestLast, "b-new"},
		"newest listed first": {"", newestFirst, ""},
		"a delayed default":   {"", []runtime.Object{delayedDefault()}, ""},
		"no default":          {"", []runtime.Object{bindingClass("other", immediate, false)}, ""},
		"named over default": {"fast", []runtime.Object{
			bindingClass("standard", immediate, true), bindingClass("fast", delayed, false),
		}, ""},
		"named Immediate":     {"fast", []runtime.Object{delayedDefault(), bindingClass("fast", immediate, false)}, "fast"},
		"named and not found": {"fast", []runtime.Object{bindingClass("standard", immediate, true)}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(storageClassEnv, tc.named)
			objs := append(threeZones(3), tc.classes...)
			s := runTopology(t, HAModePreferred, false, objs...)
			requireRow(t, s, rowPass)
			if tc.judged == "" {
				assert.Empty(t, s.Warnings)
				return
			}
			warnings := topologyWarnings(s)
			require.Len(t, warnings, 1)
			assert.Contains(t, warnings[0], "StorageClass "+tc.judged+" binds volumes Immediate")
			assert.Contains(t, warnings[0], "mode preferred asks for WaitForFirstConsumer (best-effort)")
			assert.Contains(t, s.Recommendations, haStorageAdvice)

			requireRow(t, runTopology(t, HAModeEnforced, false, objs...), rowFail)
		})
	}
}

// A read that fails leaves the row unknown with a warning naming it, unless
// what the run did read already fails the row.
func TestCheckHATopology_UnreadIsUnknown(t *testing.T) {
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "x"}, "", errors.New("denied"))
	healthy := func() []runtime.Object {
		objs := append(threeZones(3), installedQuorum(corev1.PodSpec{})...)
		return append(objs, tier1Deployment("nvcf", "api", "api", 2, corev1.PodSpec{}), delayedDefault())
	}
	for _, resource := range []string{"nodes", "statefulsets", "deployments", "storageclasses"} {
		t.Run(resource, func(t *testing.T) {
			client := fake.NewSimpleClientset(healthy()...)
			failWith(client, "list", resource, denied)
			s := runTopologyWith(t, client, HAModeEnforced, true)
			requireRow(t, s, nil)
			warnings := topologyWarnings(s)
			require.NotEmpty(t, warnings)
			assert.True(t, strings.HasPrefix(warnings[len(warnings)-1], "status unknown"), warnings)
			assert.Contains(t, warnings[len(warnings)-1], "grant the cluster-validator ServiceAccount")
		})
	}
	t.Run("named StorageClass", func(t *testing.T) {
		t.Setenv(storageClassEnv, "fast")
		client := fake.NewSimpleClientset(healthy()...)
		failWith(client, "get", "storageclasses", denied)
		requireRow(t, runTopologyWith(t, client, HAModeEnforced, true), nil)
	})
	t.Run("a failure stands", func(t *testing.T) {
		client := fake.NewSimpleClientset(append(zonedNodes(2, "zone-a", "zone-b"), delayedDefault())...)
		failWith(client, "list", "statefulsets", denied)
		requireRow(t, runTopologyWith(t, client, HAModePreferred, true), rowFail)
	})
}

// A critical row: a failure or an unknown under an HA mode blocks the verdict.
func TestHATopologyRow_BlocksTheVerdictUnderAnHAMode(t *testing.T) {
	for mode, want := range map[HAMode]bool{HAModePreferred: true, HAModeEnforced: true, HAModeNone: false, "": false} {
		state := healthyControlPlaneState()
		state.HAMode, state.HATopologyOK = mode, rowFail
		var notReady *NotReadyError
		if !want {
			assert.NoError(t, printSummary(state), mode)
			continue
		}
		require.ErrorAs(t, printSummary(state), &notReady, mode)
		assert.Equal(t, NotReadyError{Failed: 1}, *notReady, mode)

		state = healthyControlPlaneState()
		state.HAMode, state.HATopologyOK = mode, nil
		require.ErrorAs(t, printSummary(state), &notReady, mode)
		assert.Equal(t, NotReadyError{Unobserved: 1}, *notReady, mode)
		assert.Equal(t, haTopologyLabel+notFullyObserved, haTopologyRow(state).UnknownMsg)

		state.HATopologyOK = rowPass
		require.NoError(t, printSummary(state), mode)
	}
}

// The summary carries ha_topology when the row has a result, and leaves it
// out when the row did not apply or was not observed.
func TestBuildSummary_HATopology(t *testing.T) {
	state := healthyControlPlaneState()
	state.HAMode, state.HATopologyOK = HAModeEnforced, rowFail
	s := buildSummary(state, time.Now(), false, VerdictNotReady)
	require.Contains(t, s.Checks, CheckKeyHATopology)
	assert.False(t, s.Checks[CheckKeyHATopology])

	state.HATopologyOK = nil
	assert.NotContains(t, buildSummary(state, time.Now(), false, VerdictNotReady).Checks, CheckKeyHATopology)
}

// The control-plane run reads NVCF_HA_MODE, ignoring case and space, and the
// row it decides reaches the summary; without a valid mode it does not.
func TestRun_HATopologyFollowsNVCFHAMode(t *testing.T) {
	for env, want := range map[string]*bool{" Preferred ": rowFail, "": nil, "none": nil, "best-effort": nil} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(HAModeEnv, env)
			s := runAndReadSummary(t, fake.NewSimpleClientset(makeNode("node-1", true, 0)), RoleControlPlane)
			if want == nil {
				assert.NotContains(t, s.Checks, CheckKeyHATopology)
				return
			}
			require.Contains(t, s.Checks, CheckKeyHATopology)
			assert.Equal(t, *want, s.Checks[CheckKeyHATopology], "one node cannot hold a quorum")
			assert.False(t, s.VerdictReady)
		})
	}
	t.Run("compute-plane role", func(t *testing.T) {
		t.Setenv(HAModeEnv, string(HAModeEnforced))
		s := runAndReadSummary(t, fake.NewSimpleClientset(makeNode("node-1", true, 8)), RoleComputePlane)
		assert.NotContains(t, s.Checks, CheckKeyHATopology, "the HA rows judge the control plane only")
	})
}
