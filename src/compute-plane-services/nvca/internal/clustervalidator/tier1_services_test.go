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
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// releaseLabels are the pod labels release's chart renders, named name.
func releaseLabels(release, name string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": name, haInstanceLabel: release}
}

// releaseDeployment is a Deployment of release in ns named after it whose
// ready of its replicas are Ready, with its pods, labelled podLabels.
func releaseDeployment(ns, release string, podLabels map[string]string, replicas, ready int32) []runtime.Object {
	return namedDeployment(ns, release, release, podLabels, replicas, ready)
}

// namedDeployment is releaseDeployment for a Deployment called name.
func namedDeployment(ns, name, release string, podLabels map[string]string, replicas, ready int32) []runtime.Object {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1,
			Annotations: map[string]string{helmReleaseAnnotation: release},
			Labels:      map[string]string{haInstanceLabel: release}},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(podLabels)},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: maps.Clone(podLabels)}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: replicas, UpdatedReplicas: replicas,
			ReadyReplicas: ready, AvailableReplicas: ready},
	}
	objs := []runtime.Object{d}
	controller := true
	for i := range replicas {
		labels := maps.Clone(podLabels)
		labels[appsv1.DefaultDeploymentUniqueLabelKey] = "abc12"
		objs = append(objs, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-abc12-" + string(rune('a'+i)), Namespace: ns,
				Labels: labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet",
					Name: name + "-abc12", UID: types.UID("rs-" + name), Controller: &controller}}},
			Spec:   corev1.PodSpec{NodeName: "node-1"},
			Status: readyPodStatus(i < ready),
		})
	}
	return objs
}

// releaseService is a ClusterIP Service of release in ns selecting selector.
func releaseService(ns, name, release string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Annotations: map[string]string{helmReleaseAnnotation: release}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector},
	}
}

// runTier1Services runs Tier-1 after install or before it over objs, with an
// NVCF Gateway another implementation runs so only the namespaces decide the
// row, and returns its state, log and client.
func runTier1Services(t *testing.T, postInstall bool, react func(*fake.Clientset), objs ...runtime.Object) (
	*ValidationState, string, *fake.Clientset,
) {
	t.Helper()
	t.Setenv(envoyGatewayNamespaceEnv, "")
	t.Setenv(openBaoNamespaceEnv, "")
	t.Setenv(nvcfGatewayNamesEnv, "nvcf/nvcf-gw")
	client := gatewayDiscoveryClient(gatewayRequiredPairs()...)
	for _, o := range objs {
		require.NoError(t, client.Tracker().Add(o))
	}
	if react != nil {
		react(client)
	}
	log, buf := bufferLog()
	state := &ValidationState{Log: log, PostInstall: postInstall}
	checkTier1Deployments(context.Background(), client, envoyGatewayClient(t, gatewayObject("nvcf", "nvcf-gw",
		"istio")), state)
	return state, buf.String(), client
}

// An upgrade that changes the pod labels a Service selects, but cannot change
// the Deployment's immutable selector, leaves the Service selecting no pod: its
// route fails although the Deployment is Ready. After install the Tier-1 row
// fails, naming the Service, its selector and the labels the pods carry.
func TestCheckTier1Deployments_ServiceSelectingNoneOfItsReleasesPodsFails(t *testing.T) {
	old := releaseLabels("reval", "reval")
	renamed := releaseLabels("reval", "helm-reval")
	objs := append(releaseDeployment("nvcf", "reval", old, 1, 1), releaseService("nvcf", "reval", "reval", renamed))

	state, log, _ := runTier1Services(t, true, nil, objs...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, log, "Service nvcf/reval selects app.kubernetes.io/instance=reval,"+
		"app.kubernetes.io/name=helm-reval, which matches no pod, while the 1 Ready pod(s) of Deployment "+
		"nvcf/reval of release reval are labelled app.kubernetes.io/instance=reval,app.kubernetes.io/name=reval")
	assert.Contains(t, strings.Join(state.Recommendations, "; "), "helm -n <namespace> history <release>")

	objs = append(releaseDeployment("nvcf", "reval", old, 1, 1), releaseService("nvcf", "reval", "reval", old))
	state, log, _ = runTier1Services(t, true, nil, objs...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "a Service that selects its pods")
	assert.NotContains(t, log, "matches no pod")

	objs = append(releaseDeployment("nvcf", "reval", old, 1, 1), releaseService("nvcf", "reval", "reval", renamed))
	state, log, _ = runTier1Services(t, false, nil, objs...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.True(t, *state.Tier1DeploymentsOK, "before install a release may be part way through")
	assert.NotContains(t, log, "matches no pod")
}

// A Service whose pods are down is the readiness finding's to report, not a
// second finding; one whose release has no Ready pod has nothing that shows
// its selector is wrong.
func TestCheckTier1Deployments_ServiceOfPodsNotReadyIsNoSecondFinding(t *testing.T) {
	old := releaseLabels("reval", "reval")
	for name, svcSelector := range map[string]map[string]string{
		"selects the pods": old,
		"selects no pod":   releaseLabels("reval", "helm-reval"),
	} {
		objs := append(releaseDeployment("nvcf", "reval", old, 2, 0), releaseService("nvcf", "reval", "reval",
			svcSelector))
		state, log, _ := runTier1Services(t, true, nil, objs...)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.False(t, *state.Tier1DeploymentsOK, name)
		assert.Contains(t, log, "nvcf/reval (ready: 0, want: 2)", name)
		assert.NotContains(t, log, "matches no pod", name)
	}
}

// Only a pod that serves, or may yet serve, shows a Service's selector is
// right. A finished pod, such as the api chart's account-bootstrap hook pod,
// which carries its release's labels and stays Completed, and a terminating
// pod serve nothing: one of them matching the Service still leaves the gap. A
// pod that is down but may yet serve is left to the readiness findings.
func TestCheckTier1Deployments_FinishedOrTerminatingPodsDoNotHideAGap(t *testing.T) {
	old := releaseLabels("reval", "reval")
	renamed := releaseLabels("reval", "helm-reval")
	controller := true
	pod := func(name string, status corev1.PodStatus, deleted bool) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "nvcf", Labels: maps.Clone(renamed),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "reval-hook",
					UID: types.UID("job"), Controller: &controller}}},
			Spec:   corev1.PodSpec{NodeName: "node-1"},
			Status: status,
		}
		if deleted {
			now := metav1.Now()
			p.DeletionTimestamp = &now
		}
		return p
	}
	for name, tc := range map[string]struct {
		pod *corev1.Pod
		gap bool
	}{
		"Completed hook pod": {pod("reval-hook-a", corev1.PodStatus{Phase: corev1.PodSucceeded}, false), true},
		"Failed pod":         {pod("reval-hook-b", corev1.PodStatus{Phase: corev1.PodFailed}, false), true},
		"terminating pod":    {pod("reval-hook-c", readyPodStatus(true), true), true},
		"Pending pod":        {pod("reval-hook-d", corev1.PodStatus{Phase: corev1.PodPending}, false), false},
	} {
		objs := append(releaseDeployment("nvcf", "reval", old, 1, 1), releaseService("nvcf", "reval", "reval", renamed),
			tc.pod)
		state, log, _ := runTier1Services(t, true, nil, objs...)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.Equal(t, !tc.gap, *state.Tier1DeploymentsOK, name)
		if tc.gap {
			assert.Contains(t, log, "Service nvcf/reval selects app.kubernetes.io/instance=reval,"+
				"app.kubernetes.io/name=helm-reval, which matches no pod, while the 1 Ready pod(s) of Deployment "+
				"nvcf/reval of release reval are labelled", name)
		} else {
			assert.NotContains(t, log, "matches no pod", name)
		}
	}
}

// An upgrade that adds a key to a Service's selector, which the pods it ran do
// not carry, leaves the Service selecting none of them, as a changed value
// does. A key the pods gain at run time is no such gap: OpenBao's standby
// Service selects none while its only server is the leader, and neither the
// server, whose pods carry the active label with the other value, nor the
// agent injector of the same release, whose pods never carry it, is named.
// Server pods without the label, as without OpenBao's Kubernetes service
// registration, leave the Service with no endpoint.
func TestCheckTier1Deployments_SelectorKeysThePodsLack(t *testing.T) {
	old := releaseLabels("reval", "reval")
	added := maps.Clone(old)
	added["app.kubernetes.io/component"] = "server"
	objs := append(releaseDeployment("nvcf", "reval", old, 1, 1), releaseService("nvcf", "reval", "reval", added))
	state, log, _ := runTier1Services(t, true, nil, objs...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, log, "Service nvcf/reval selects app.kubernetes.io/component=server,"+
		"app.kubernetes.io/instance=reval,app.kubernetes.io/name=reval, which matches no pod, while the 1 Ready "+
		"pod(s) of Deployment nvcf/reval of release reval are labelled app.kubernetes.io/instance=reval,"+
		"app.kubernetes.io/name=reval, with no app.kubernetes.io/component label")

	server := releaseLabels("openbao-server", "openbao")
	server["component"] = "server"
	injector := releaseLabels("openbao-server", "openbao-agent-injector")
	injector["component"] = "webhook"
	standby := maps.Clone(server)
	standby["openbao-active"] = "false"
	// The injector is named to list before the server and after it.
	openBao := func(registered bool, injectorName string) []runtime.Object {
		objs := append(releaseDeployment("nvcf", "reval", old, 1, 1),
			namedDeployment("vault-system", "openbao-server", "openbao-server", server, 1, 1)...)
		for _, o := range objs {
			if p, ok := o.(*corev1.Pod); ok && registered {
				p.Labels["openbao-active"] = "true"
			}
		}
		objs = append(objs, namedDeployment("vault-system", injectorName, "openbao-server", injector, 2, 2)...)
		return append(objs, releaseService("vault-system", "openbao-server-standby", "openbao-server", standby))
	}
	for _, injectorName := range []string{"agent-injector", "openbao-server-agent-injector"} {
		state, log, _ = runTier1Services(t, true, nil, openBao(true, injectorName)...)
		require.NotNil(t, state.Tier1DeploymentsOK, injectorName)
		assert.True(t, *state.Tier1DeploymentsOK, log)
		assert.NotContains(t, log, "matches no pod", injectorName)

		// The run-time key does not hide a changed value beside it.
		standby["app.kubernetes.io/name"] = "openbao-server"
		state, log, _ = runTier1Services(t, true, nil, openBao(true, injectorName)...)
		require.NotNil(t, state.Tier1DeploymentsOK, injectorName)
		assert.False(t, *state.Tier1DeploymentsOK, injectorName)
		assert.Contains(t, log, "while the 1 Ready pod(s) of Deployment vault-system/openbao-server of release "+
			"openbao-server are labelled app.kubernetes.io/instance=openbao-server,app.kubernetes.io/name=openbao,"+
			"component=server\n", injectorName)
		standby["app.kubernetes.io/name"] = "openbao"

		state, log, _ = runTier1Services(t, true, nil, openBao(false, injectorName)...)
		require.NotNil(t, state.Tier1DeploymentsOK, injectorName)
		assert.False(t, *state.Tier1DeploymentsOK, injectorName)
		assert.Contains(t, log, "Service vault-system/openbao-server-standby selects app.kubernetes.io/instance="+
			"openbao-server,app.kubernetes.io/name=openbao,component=server,openbao-active=false, which matches no "+
			"pod, while the 1 Ready pod(s) of Deployment vault-system/openbao-server of release openbao-server are "+
			"labelled app.kubernetes.io/instance=openbao-server,app.kubernetes.io/name=openbao,component=server, "+
			"with no openbao-active label", injectorName)
	}
}

// Only a Service of a stack release whose selector names labels its release's
// pods carry with other values is judged. One that names none of them with
// the value they carry, one whose StatefulSet is scaled to zero, the headless
// Service a StatefulSet governs, an ExternalName or selector-less Service, one
// of no release, and another install's Service in a shared namespace are not.
func TestCheckTier1Deployments_ServicesThatAreNotSelectorMismatches(t *testing.T) {
	old := releaseLabels("reval", "reval")
	renamed := releaseLabels("reval", "helm-reval")
	zero := int32(0)
	scaledIn := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "reval-cache", Namespace: "nvcf",
			Annotations: map[string]string{helmReleaseAnnotation: "reval"}},
		Spec: appsv1.StatefulSetSpec{Replicas: &zero, ServiceName: "reval-cache",
			Selector: &metav1.LabelSelector{MatchLabels: renamed},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: renamed}}},
	}
	headless := releaseService("nvcf", "reval-peers", "reval", renamed)
	headless.Spec.ClusterIP = corev1.ClusterIPNone
	governing := scaledIn.DeepCopy()
	governing.Name, governing.Spec.ServiceName = "reval-peers", "reval-peers"
	governing.Spec.Template.Labels = releaseLabels("reval", "peers")
	external := releaseService("nvcf", "reval-ext", "reval", nil)
	external.Spec.Type, external.Spec.ExternalName = corev1.ServiceTypeExternalName, "reval.example.com"
	unreleased := releaseService("nvcf", "reval-manual", "reval", renamed)
	unreleased.Annotations = nil
	foreign := append(releaseDeployment("cert-manager", "x", releaseLabels("x", "x"), 1, 1),
		releaseService("cert-manager", "webhook", "someone-else", releaseLabels("x", "webhook")))
	for name, extra := range map[string][]runtime.Object{
		"none of its pods' labels": {releaseService("nvcf", "reval-metrics", "reval",
			map[string]string{"app": "metrics"})},
		"a StatefulSet scaled to 0":  {releaseService("nvcf", "reval-cache", "reval", renamed), scaledIn},
		"a StatefulSet's governing":  {headless, governing},
		"ExternalName":               {external},
		"no selector":                {releaseService("nvcf", "reval-none", "reval", nil)},
		"no release":                 {unreleased},
		"another install, shared ns": foreign,
	} {
		objs := append(releaseDeployment("nvcf", "reval", old, 1, 1), extra...)
		state, log, _ := runTier1Services(t, true, nil, objs...)
		require.NotNil(t, state.Tier1DeploymentsOK, name)
		assert.True(t, *state.Tier1DeploymentsOK, "%s: %s", name, log)
		assert.NotContains(t, log, "matches no pod", name)
	}

	// A Service of release x installed by the stack in a shared namespace is
	// judged like any other.
	stack := releaseService("cert-manager", "cert-manager", "cert-manager", releaseLabels("cert-manager", "cm"))
	stack.Annotations["meta.helm.sh/release-namespace"] = "cert-manager"
	deploy := releaseDeployment("cert-manager", "cert-manager", releaseLabels("cert-manager", "cert-manager"), 1, 1)
	deploy[0].(*appsv1.Deployment).Annotations["meta.helm.sh/release-namespace"] = "cert-manager"
	objs := append(releaseDeployment("nvcf", "reval", old, 1, 1), append(deploy, stack)...)
	state, log, _ := runTier1Services(t, true, nil, objs...)
	require.NotNil(t, state.Tier1DeploymentsOK)
	assert.False(t, *state.Tier1DeploymentsOK)
	assert.Contains(t, log, "Service cert-manager/cert-manager selects")
}

// Services or pods it could not list leave the row unknown, not passed: a
// Service with no endpoint would go unseen. A failure seen elsewhere still
// fails it.
func TestCheckTier1Deployments_UnreadableServicesAreUnknown(t *testing.T) {
	old := releaseLabels("reval", "reval")
	for resource, unread := range map[string]string{"services": "Services in nvcf", "pods": "pods in nvcf"} {
		deny := func(c *fake.Clientset) {
			c.PrependReactor("list", resource, func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() == "nvcf" {
					return true, nil, apierrors.NewForbidden(corev1.Resource(resource), "", errors.New("denied"))
				}
				return false, nil, nil
			})
		}
		objs := append(releaseDeployment("nvcf", "reval", old, 1, 1), releaseService("nvcf", "reval", "reval", old))
		state, _, _ := runTier1Services(t, true, deny, objs...)
		assert.Nil(t, state.Tier1DeploymentsOK, resource)
		assert.Contains(t, strings.Join(state.Warnings, "; "), "Tier-1 Deployments: could not read "+unread,
			resource)

		objs = append(releaseDeployment("nvcf", "reval", old, 2, 1), releaseService("nvcf", "reval", "reval", old))
		state, _, _ = runTier1Services(t, true, deny, objs...)
		require.NotNil(t, state.Tier1DeploymentsOK, resource)
		assert.False(t, *state.Tier1DeploymentsOK, resource)
	}
}

// Tier-1 lists the Services and pods of each namespace at most once, and the
// StatefulSets only of a namespace with a Service that selects no pod.
func TestCheckTier1Deployments_ServiceReadsAreBounded(t *testing.T) {
	old := releaseLabels("reval", "reval")
	objs := append(releaseDeployment("nvcf", "reval", old, 1, 1),
		releaseService("nvcf", "reval", "reval", releaseLabels("reval", "helm-reval")),
		releaseService("nvcf", "reval-2", "reval", releaseLabels("reval", "helm-reval-2")),
		releaseService("nvcf", "reval-ok", "reval", old))
	objs = append(objs, releaseDeployment("sis", "sis", releaseLabels("sis", "sis"), 1, 1)...)
	objs = append(objs, releaseService("sis", "sis", "sis", releaseLabels("sis", "sis")))
	_, _, client := runTier1Services(t, true, nil, objs...)
	lists := map[string]int{}
	for _, a := range client.Actions() {
		if r := a.GetResource().Resource; a.GetVerb() == "list" && (r == "services" || r == "pods" ||
			r == "statefulsets") {
			lists[r+"/"+a.GetNamespace()]++
		}
	}
	for key, n := range lists {
		assert.Equal(t, 1, n, key)
	}
	assert.Contains(t, lists, "services/nvcf")
	assert.Contains(t, lists, "pods/nvcf")
	assert.Contains(t, lists, "statefulsets/nvcf")
	assert.Contains(t, lists, "pods/sis")
	assert.NotContains(t, lists, "statefulsets/sis", "every Service in sis selects a pod")
	assert.NotContains(t, lists, "pods/vault-system", "no Service there to judge")
}
