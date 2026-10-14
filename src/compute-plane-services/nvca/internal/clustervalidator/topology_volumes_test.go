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
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// localClass is the StorageClass of the node-local volumes in these tests,
// not the default one the row judges, unless a test makes it so.
const localClass = "local-path"

// claimed gives sts n members and a volume claim template, as the stack's
// quorum StatefulSets render one.
func claimed(sts *appsv1.StatefulSet, n int32) *appsv1.StatefulSet {
	sts.Spec.Replicas = &n
	sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data"}}}
	return sts
}

// memberVolume is the claim of sts's member ordinal, bound to a volume of
// class whose required node affinity is terms.
func memberVolume(sts *appsv1.StatefulSet, ordinal int, class string, terms ...corev1.NodeSelectorTerm) (
	*corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {
	name := fmt.Sprintf("data-%s-%d", sts.Name, ordinal)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sts.Namespace, UID: types.UID("uid-" + name)},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pv-" + name, StorageClassName: &class},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name},
		Spec: corev1.PersistentVolumeSpec{StorageClassName: class,
			ClaimRef: &corev1.ObjectReference{Namespace: sts.Namespace, Name: name, UID: pvc.UID}},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	if len(terms) > 0 {
		pv.Spec.NodeAffinity = &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{NodeSelectorTerms: terms}}
	}
	return pvc, pv
}

func onHost(host string) corev1.NodeSelectorTerm {
	return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{host}}}}
}

func inZone(zone string) corev1.NodeSelectorTerm {
	return corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{zone}}}}
}

// pinned binds member i of sts to a node-local volume on hosts[i]; an empty
// host leaves that member without a claim.
func pinned(sts *appsv1.StatefulSet, hosts ...string) []runtime.Object {
	var objs []runtime.Object
	for i, h := range hosts {
		if h != "" {
			pvc, pv := memberVolume(sts, i, localClass, onHost(h))
			objs = append(objs, pvc, pv)
		}
	}
	return objs
}

// pinnedCluster is four nodes over three zones, two in zone-b, the quorum
// with NATS claiming volumes, and the default StorageClass.
func pinnedCluster(nats *appsv1.StatefulSet, volumes ...runtime.Object) []runtime.Object {
	objs := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"), topoNode("node-3", "zone-b"),
		topoNode("node-4", "zone-c"), nats, openBaoSet(corev1.PodSpec{}), cassandraSet(corev1.PodSpec{}),
		delayedDefault()}
	return append(objs, volumes...)
}

// A quorum member whose bound volume requires one node by hostname, as a
// node-local volume does, can only run there. Pinned members that share a
// node, or crowd a zone beyond what a zone spread of maxSkew 1 allows, fail
// under enforced and warn under preferred, with a warning that such a member
// cannot be recreated elsewhere when its node fails. Members pinned one per
// zone, members not pinned, and volumes that only require a zone are fine.
func TestCheckHATopology_NodeLocalVolumesPinMembers(t *testing.T) {
	const recreate = "nats-system/nats: nats-0, nats-1 and nats-2 cannot be recreated on another node if their " +
		"node fails: their volumes are node-local"
	nats := func() *appsv1.StatefulSet { return claimed(natsSet(corev1.PodSpec{}), 3) }
	zonal := func() []runtime.Object {
		var objs []runtime.Object
		for i, z := range []string{"zone-a", "zone-b", "zone-c"} {
			pvc, pv := memberVolume(nats(), i, localClass, inZone(z))
			objs = append(objs, pvc, pv)
		}
		return objs
	}
	released := func() []runtime.Object {
		objs := pinned(nats(), "node-2", "node-3", "node-1")
		objs[1].(*corev1.PersistentVolume).Spec.ClaimRef.UID = "an-earlier-claim"
		return objs
	}
	for name, tc := range map[string]struct {
		volumes  []runtime.Object
		findings []string
		warning  string
	}{
		"one per zone": {volumes: pinned(nats(), "node-1", "node-2", "node-4")},
		"zone-b crowded": {volumes: pinned(nats(), "node-2", "node-3", "node-1"), findings: []string{
			"nats-system/nats: node-local volumes pin members per zone zone-a 1, zone-b 2, zone-c 0 (nats-0 on " +
				"node-2, nats-1 on node-3, nats-2 on node-1); zone-b holds more than a zone spread of maxSkew 1 " +
				"allows for 3 members over 3 zones (1 per zone); "}, warning: recreate},
		"one node": {volumes: pinned(nats(), "node-1", "node-1", "node-2"), findings: []string{
			"nats-system/nats: node-local volumes pin members nats-0 and nats-1 to node node-1; ",
			"nats-system/nats: node-local volumes pin members per zone zone-a 2, zone-b 1, zone-c 0 "},
			warning: recreate},
		"two of three pinned": {volumes: pinned(nats(), "node-2", "node-3", ""), findings: []string{
			"nats-system/nats: node-local volumes pin members per zone zone-a 0, zone-b 2, zone-c 0 (nats-0 on " +
				"node-2, nats-1 on node-3); zone-b holds more"},
			warning: "nats-system/nats: nats-0 and nats-1 cannot be recreated on another node"},
		"zonal volumes":          {volumes: zonal()},
		"a volume of an earlier": {volumes: released()},
	} {
		t.Run(name, func(t *testing.T) {
			s, log := runTopologyLogged(t, HAModeEnforced, true, pinnedCluster(nats(), tc.volumes...)...)
			if len(tc.findings) == 0 {
				requireRow(t, s, rowPass)
				assert.Empty(t, s.Warnings)
				return
			}
			requireRow(t, s, rowFail)
			for _, f := range tc.findings {
				assert.Contains(t, log, f)
			}
			assert.Contains(t, log, "mode enforced requires")
			warnings := topologyWarnings(s)
			require.Len(t, warnings, 1)
			assert.True(t, strings.HasPrefix(warnings[0], tc.warning), warnings[0])
			assert.Contains(t, s.Recommendations, haPinnedAdvice)

			s, _ = runTopologyLogged(t, HAModePreferred, true, pinnedCluster(nats(), tc.volumes...)...)
			requireRow(t, s, rowPass)
			warnings = topologyWarnings(s)
			require.Len(t, warnings, len(tc.findings)+1)
			for i, f := range tc.findings {
				assert.True(t, strings.HasPrefix(warnings[i], f), warnings[i])
				assert.True(t, strings.HasSuffix(warnings[i], "(best-effort)"), warnings[i])
			}
		})
	}
}

// With more members than zones, maxSkew 1 lets some zones hold one more: a
// 5-member quorum over 3 zones fits as 2, 2 and 1, so members pinned that way
// are fine and 3 pinned in one zone are not.
func TestCheckHATopology_NodeLocalVolumesUnevenZones(t *testing.T) {
	nodes := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-a"),
		topoNode("node-3", "zone-a"), topoNode("node-4", "zone-b"), topoNode("node-5", "zone-b"),
		topoNode("node-6", "zone-c"), openBaoSet(corev1.PodSpec{}), cassandraSet(corev1.PodSpec{}), delayedDefault()}
	nats := func() *appsv1.StatefulSet { return claimed(natsSet(corev1.PodSpec{}), 5) }

	objs := append(append(nodes, nats()), pinned(nats(), "node-1", "node-4", "node-2", "node-5", "node-6")...)
	s := runTopology(t, HAModeEnforced, true, objs...)
	requireRow(t, s, rowPass)
	assert.Empty(t, s.Warnings)

	objs = append(append(nodes, nats()), pinned(nats(), "node-1", "node-2", "node-3", "node-4", "node-6")...)
	s, log := runTopologyLogged(t, HAModeEnforced, true, objs...)
	requireRow(t, s, rowFail)
	assert.Contains(t, log, "node-local volumes pin members per zone zone-a 3, zone-b 1, zone-c 1 (")
	assert.Contains(t, log, "zone-a holds more than a zone spread of maxSkew 1 allows for 5 members over 3 zones "+
		"(1 or 2 per zone, 2 in at most 2 zone(s)); mode enforced requires that spread, under which a member "+
		"recreated in zone-a cannot be placed")
}

// A member a node-local volume pins to a node its service cannot run on, one
// cordoned, NotReady, removed or outside its placement, cannot run on any
// node: it fails under enforced and warns under preferred, with the warning
// that pinned members cannot be recreated elsewhere. The other pinned members
// are still judged for the zone spread.
func TestCheckHATopology_MembersPinnedToNodesTheyCannotRunOn(t *testing.T) {
	const unusable = "nats-system/nats: a node-local volume pins nats-2 to node node-3, which "
	notReady := func(n *corev1.Node) {
		n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
	}
	nats := func(spec corev1.PodSpec) *appsv1.StatefulSet { return claimed(natsSet(spec), 3) }
	cluster := func(node3 *corev1.Node, sts *appsv1.StatefulSet, hosts ...string) []runtime.Object {
		objs := []runtime.Object{topoNode("node-1", "zone-a"), topoNode("node-2", "zone-b"),
			topoNode("node-4", "zone-c"), topoNode("node-5", "zone-a"), sts, openBaoSet(corev1.PodSpec{}),
			cassandraSet(corev1.PodSpec{}), delayedDefault()}
		if node3 != nil {
			objs = append(objs, node3)
		}
		return append(objs, pinned(sts, hosts...)...)
	}
	pooled := func(objs []runtime.Object) []runtime.Object {
		for _, o := range objs {
			if n, ok := o.(*corev1.Node); ok && n.Name != "node-3" {
				n.Labels["pool"] = "nats"
			}
		}
		return objs
	}
	inPool := corev1.PodSpec{NodeSelector: map[string]string{"pool": "nats"}}
	for name, tc := range map[string]struct {
		objs     []runtime.Object
		findings []string
	}{
		"cordoned": {cluster(topoNode("node-3", "zone-c", func(n *corev1.Node) { n.Spec.Unschedulable = true }),
			nats(corev1.PodSpec{}), "node-1", "node-2", "node-3"),
			[]string{unusable + "is not usable (cordoned), so it cannot run on any node; "}},
		"NotReady": {cluster(topoNode("node-3", "zone-c", notReady), nats(corev1.PodSpec{}), "node-1", "node-2",
			"node-3"), []string{unusable + "is not usable (NotReady), so it cannot run on any node; "}},
		"removed": {cluster(nil, nats(corev1.PodSpec{}), "node-1", "node-2", "node-3"),
			[]string{unusable + "is not in the cluster, so it cannot run on any node; "}},
		"outside its placement": {pooled(cluster(topoNode("node-3", "zone-c"), nats(inPool), "node-1", "node-2",
			"node-3")), []string{unusable + "its placement does not admit (nodeSelector pool=nats), so it cannot " +
			"run on any node; "}},
		"removed, the rest crowd a zone": {cluster(nil, nats(corev1.PodSpec{}), "node-1", "node-5", "node-3"),
			[]string{
				unusable + "is not in the cluster, so it cannot run on any node; ",
				"nats-system/nats: node-local volumes pin members per zone zone-a 2, zone-b 0, zone-c 0 (nats-0 on " +
					"node-1, nats-1 on node-5); zone-a holds more than a zone spread of maxSkew 1 allows for 3 " +
					"members over 3 zones (1 per zone); ",
			}},
	} {
		t.Run(name, func(t *testing.T) {
			s, log := runTopologyLogged(t, HAModeEnforced, true, tc.objs...)
			requireRow(t, s, rowFail)
			for _, f := range tc.findings {
				assert.Contains(t, log, f)
			}
			assert.Contains(t, log, "mode enforced requires every member on a node it can run on")
			warnings := topologyWarnings(s)
			require.Len(t, warnings, 1)
			assert.True(t, strings.HasPrefix(warnings[0], "nats-system/nats: nats-0, nats-1 and nats-2 cannot be "+
				"recreated on another node if their node fails"), warnings[0])
			assert.Contains(t, s.Recommendations, haPinnedAdvice)

			s, _ = runTopologyLogged(t, HAModePreferred, true, tc.objs...)
			requireRow(t, s, rowPass)
			warnings = topologyWarnings(s)
			require.Len(t, warnings, len(tc.findings)+1)
			for i, f := range tc.findings {
				assert.True(t, strings.HasPrefix(warnings[i], f), warnings[i])
				assert.True(t, strings.HasSuffix(warnings[i], "(best-effort)"), warnings[i])
			}
		})
	}
}

// The StorageClass the stack's volumes bind to warns, under either mode, when
// it provisions node-local volumes, as its bound volumes that require one node
// by hostname show: whatever the provisioner is named, and with
// WaitForFirstConsumer, which no longer passes it alone. A volume of another
// class, one that requires a zone, and one not bound say nothing of it.
func TestCheckHATopology_NodeLocalStorageClassWarns(t *testing.T) {
	local := func(class string, phase corev1.PersistentVolumePhase, terms ...corev1.NodeSelectorTerm) runtime.Object {
		_, pv := memberVolume(natsSet(corev1.PodSpec{}), 0, class, terms...)
		pv.Name, pv.Status.Phase = "pvc-1234", phase
		return pv
	}
	bound := func(terms ...corev1.NodeSelectorTerm) runtime.Object {
		return local(localClass, corev1.VolumeBound, terms...)
	}
	localPath := func() runtime.Object {
		sc := bindingClass(localClass, bindingMode(storagev1.VolumeBindingWaitForFirstConsumer), true)
		sc.Provisioner = "example.com/any-name"
		return sc
	}
	for name, tc := range map[string]struct {
		named string
		objs  []runtime.Object
		warns bool
	}{
		"node-local default": {"", []runtime.Object{localPath(), bound(onHost("node-1"))}, true},
		"node-local named": {localClass, []runtime.Object{delayedDefault(), localPath(), bound(onHost("node-1"))},
			true},
		"zonal volume":    {"", []runtime.Object{localPath(), bound(inZone("zone-a"))}, false},
		"another class's": {"", []runtime.Object{delayedDefault(), bound(onHost("node-1"))}, false},
		"not bound": {"", []runtime.Object{localPath(), local(localClass, corev1.VolumeReleased,
			onHost("node-1"))}, false},
		"no volume to tell by":  {"", []runtime.Object{localPath()}, false},
		"two hostnames allowed": {"", []runtime.Object{localPath(), bound(onHost("node-1"), onHost("node-2"))}, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(storageClassEnv, tc.named)
			byMode(t, func(t *testing.T, mode HAMode) {
				s := runTopology(t, mode, false, append(threeZones(3), tc.objs...)...)
				requireRow(t, s, rowPass)
				if !tc.warns {
					assert.Empty(t, s.Warnings)
					return
				}
				warnings := topologyWarnings(s)
				require.Len(t, warnings, 1)
				assert.Equal(t, "StorageClass local-path provisions node-local volumes: 1 bound PersistentVolume(s) "+
					"of it (pvc-1234) each require one node by kubernetes.io/hostname, so a volume cannot be "+
					"attached on another node and a quorum member whose node fails cannot be recreated elsewhere "+
					"until that node returns", warnings[0])
				assert.Contains(t, s.Recommendations, haLocalStorageAdvice)
				assert.Equal(t, haTopologyLabel+": Meets HA Mode "+string(mode)+" (with warnings)",
					haTopologyRow(s).PassMsg)
			})
		})
	}

	// An Immediate class is a finding of its own as well.
	immediate := bindingClass(localClass, bindingMode(storagev1.VolumeBindingImmediate), true)
	s := runTopology(t, HAModeEnforced, false, append(threeZones(3), immediate,
		local(localClass, corev1.VolumeBound, onHost("node-1")))...)
	requireRow(t, s, rowFail)
	require.Len(t, topologyWarnings(s), 1)
	assert.Contains(t, topologyWarnings(s)[0], "provisions node-local volumes")
}

// The volumes and claims are read once each, and only the namespaces of
// quorum services that claim volumes have their claims read.
func TestCheckHATopology_VolumeReadsAreBounded(t *testing.T) {
	nats := claimed(natsSet(corev1.PodSpec{}), 3)
	client := fake.NewSimpleClientset(pinnedCluster(nats, pinned(nats, "node-1", "node-2", "node-4")...)...)
	requireRow(t, runTopologyWith(t, client, HAModeEnforced, true), rowPass)
	count := map[string]int{}
	for _, a := range client.Actions() {
		count[a.GetVerb()+" "+a.GetResource().Resource+" "+a.GetNamespace()]++
	}
	assert.Equal(t, 1, count["list persistentvolumes "])
	assert.Equal(t, 1, count["list persistentvolumeclaims nats-system"])
	assert.Zero(t, count["list persistentvolumeclaims vault-system"], "OpenBao claims no volume here")
	for key, n := range count {
		assert.Equal(t, 1, n, "%s is read once", key)
	}

	// Before install nothing claims a volume: only the StorageClass reads them.
	client = fake.NewSimpleClientset(append(threeZones(3), delayedDefault())...)
	requireRow(t, runTopologyWith(t, client, HAModeEnforced, false), rowPass)
	for _, a := range client.Actions() {
		assert.NotEqual(t, "persistentvolumeclaims", a.GetResource().Resource)
	}
}

// Volumes or claims that cannot be read leave the row unknown with a warning
// naming them, never a pass, unless what was read already fails it.
func TestCheckHATopology_UnreadVolumesAreUnknown(t *testing.T) {
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "x"}, "", errors.New("denied"))
	nats := claimed(natsSet(corev1.PodSpec{}), 3)
	for _, resource := range []string{"persistentvolumes", "persistentvolumeclaims"} {
		t.Run(resource, func(t *testing.T) {
			client := fake.NewSimpleClientset(pinnedCluster(nats, pinned(nats, "node-1", "node-2", "node-4")...)...)
			failWith(client, "list", resource, denied)
			s := runTopologyWith(t, client, HAModeEnforced, true)
			requireRow(t, s, nil)
			warnings := topologyWarnings(s)
			require.NotEmpty(t, warnings)
			last := warnings[len(warnings)-1]
			assert.True(t, strings.HasPrefix(last, "status unknown"), last)
			assert.Contains(t, last, map[string]string{"persistentvolumes": "could not read PersistentVolumes",
				"persistentvolumeclaims": "could not read PersistentVolumeClaims in nats-system"}[resource])
			assert.Contains(t, last, "grant the cluster-validator ServiceAccount")
		})
	}
	t.Run("before install", func(t *testing.T) {
		client := fake.NewSimpleClientset(append(threeZones(3), delayedDefault())...)
		failWith(client, "list", "persistentvolumes", denied)
		requireRow(t, runTopologyWith(t, client, HAModePreferred, false), nil)
	})
	t.Run("a failure stands", func(t *testing.T) {
		client := fake.NewSimpleClientset(append(zonedNodes(2, "zone-a", "zone-b"), delayedDefault())...)
		failWith(client, "list", "persistentvolumes", denied)
		requireRow(t, runTopologyWith(t, client, HAModePreferred, false), rowFail)
	})
}
