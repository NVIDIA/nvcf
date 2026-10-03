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

package clustervalidator

import (
	"context"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

// rbacInventoryFile is the shared RBAC contract; see its header.
const rbacInventoryFile = "rbac_inventory.yaml"

// nonResourceGroup marks a non-resource URL in an apiRequest.
const nonResourceGroup = "nonResourceURL"

type inventoryRule struct {
	Roles           []Role   `json:"roles"`
	Scope           string   `json:"scope"`
	APIGroup        string   `json:"apiGroup"`
	Resources       []string `json:"resources"`
	ResourceNames   []string `json:"resourceNames"`
	NonResourceURLs []string `json:"nonResourceURLs"`
	Verbs           []string `json:"verbs"`
}

type apiRequest struct {
	group, resource, verb, namespace, name string
}

func (r apiRequest) key() string { return r.group + "|" + r.resource + "|" + r.verb }

func loadRBACInventory(t *testing.T) []inventoryRule {
	t.Helper()
	raw, err := os.ReadFile(rbacInventoryFile)
	require.NoError(t, err)
	var inv struct {
		Rules []inventoryRule `json:"rules"`
	}
	require.NoError(t, yaml.UnmarshalStrict(raw, &inv))
	require.NotEmpty(t, inv.Rules)
	for _, r := range inv.Rules {
		require.Contains(t, []string{"cluster", "namespace"}, r.Scope)
		require.NotEmpty(t, r.Roles)
		require.NotEmpty(t, r.Verbs)
		require.NotEqual(t, len(r.Resources) == 0, len(r.NonResourceURLs) == 0,
			"a rule lists resources or non-resource URLs, not both")
	}
	return inv.Rules
}

// covers reports whether r grants req to role. Namespace-scoped rules only
// cover requests in the validator's own namespace.
func (r inventoryRule) covers(role Role, req apiRequest, ownNamespace string) bool {
	if !slices.Contains(r.Roles, role) || !slices.Contains(r.Verbs, req.verb) {
		return false
	}
	if req.group == nonResourceGroup {
		return slices.Contains(r.NonResourceURLs, req.resource)
	}
	if r.APIGroup != req.group || !slices.Contains(r.Resources, req.resource) {
		return false
	}
	if len(r.ResourceNames) > 0 && !slices.Contains(r.ResourceNames, req.name) {
		return false
	}
	return r.Scope == "cluster" || req.namespace == ownNamespace
}

// keys lists every group|resource|verb the inventory grants role.
func inventoryKeys(rules []inventoryRule, role Role) []string {
	var keys []string
	for _, r := range rules {
		if !slices.Contains(r.Roles, role) {
			continue
		}
		for _, v := range r.Verbs {
			for _, res := range r.Resources {
				keys = append(keys, apiRequest{group: r.APIGroup, resource: res, verb: v}.key())
			}
			for _, url := range r.NonResourceURLs {
				keys = append(keys, apiRequest{group: nonResourceGroup, resource: url, verb: v}.key())
			}
		}
	}
	return keys
}

// requestsOf turns recorded fake-client actions into API requests. Discovery
// reads are skipped: every authenticated user may make them.
func requestsOf(actions []ktesting.Action) []apiRequest {
	var out []apiRequest
	for _, a := range actions {
		gvr := a.GetResource()
		if gvr.Group == "" && (gvr.Resource == "group" || gvr.Resource == "resource") {
			continue
		}
		if gvr.Group == "" && gvr.Resource == "version" {
			out = append(out, apiRequest{group: nonResourceGroup, resource: "/version", verb: a.GetVerb()})
			continue
		}
		req := apiRequest{group: gvr.Group, resource: gvr.Resource, verb: a.GetVerb(), namespace: a.GetNamespace()}
		if sub := a.GetSubresource(); sub != "" {
			req.resource += "/" + sub
		}
		switch act := a.(type) {
		case ktesting.GetAction:
			req.name = act.GetName()
		case ktesting.DeleteAction:
			req.name = act.GetName()
		case ktesting.UpdateAction:
			if m, err := meta.Accessor(act.GetObject()); err == nil {
				req.name = m.GetName()
			}
		}
		out = append(out, req)
	}
	return out
}

const inventoryNamespace = "nvca-operator"

// inventoryNetworkChecks is a network-checks ConfigMap with NetworkPolicy
// pairs and active enforcement, so a Run makes those requests too.
func inventoryNetworkChecks(enforcement bool) *corev1.ConfigMap {
	cfg := `networkPolicies:
  pairs:
    - name: app-to-db
      a: {namespace: app-ns, podSelector: {app: myapp}}
      b: {namespace: db-ns, podSelector: {app: postgres}}
      port: 5432
      protocol: TCP
`
	if enforcement {
		cfg += "enforcement:\n  enabled: true\n  timeoutSeconds: 5\n"
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-validator-network-checks", Namespace: inventoryNamespace},
		Data:       map[string]string{configDataKey: cfg},
	}
}

func inventoryNamespaces() []runtime.Object {
	var objs []runtime.Object
	for _, ns := range []string{"app-ns", "db-ns", inventoryNamespace} {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	}
	return objs
}

// enforcementPodsComplete makes the enforcement server Ready and every probe finish,
// so the run reaches every phase and its cleanup.
func enforcementPodsComplete(client *fake.Clientset) {
	client.PrependReactor("get", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		get := a.(ktesting.GetAction)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: get.GetName(), Namespace: get.GetNamespace()}}
		if get.GetName() == enforcementServerPod {
			pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.9",
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
		} else {
			pod.Status.Phase = corev1.PodSucceeded
		}
		return true, pod, nil
	})
}

// runValidatorTwice runs the validator twice, so the second run updates the summary
// the first created.
func runValidatorTwice(t *testing.T, client *fake.Clientset, routes *ktesting.Fake, run func()) []ktesting.Action {
	t.Helper()
	run()
	run()
	actions := client.Actions()
	if routes != nil {
		actions = append(actions, routes.Actions()...)
	}
	return actions
}

func controlPlaneRunActions(t *testing.T) []ktesting.Action {
	t.Helper()
	t.Setenv(PostInstallEnv, "true")
	t.Setenv(nvcfGatewayNamesEnv, "")
	t.Setenv(envoyGatewayNamespaceEnv, "")
	prev := enforcementPropDelay
	enforcementPropDelay = 0
	t.Cleanup(func() { enforcementPropDelay = prev })

	pairs := append(gatewayRequiredPairs(), gatewayAPIGroup+"/v1alpha2/udproutes")
	client := gatewayDiscoveryClient(pairs...)
	old := time.Now().Add(-2 * time.Hour)
	missing := makeQuorumSTS("nats", "nats-system", 3, 2, []string{"node-1", "node-2"})
	missing[0].(*appsv1.StatefulSet).Status.UpdateRevision = "nats-r2"
	two := int32(2)
	objs := append(inventoryNamespaces(), withRevision(createdAt(missing, old), old)...)
	objs = append(objs,
		makeNode("node-1", true, 0),
		inventoryNetworkChecks(true),
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard", Annotations: map[string]string{
			"storageclass.kubernetes.io/is-default-class": "true",
		}}},
		makeEnvoyControllerPod("envoy-gateway-0", true),
		gatewayLBService(envoyGatewayNamespace, "envoy-nvcf", "nvcf", "shared-gw",
			corev1.ServiceTypeLoadBalancer, "192.0.2.10"),
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "nvcf", Generation: 1},
			Spec:       appsv1.DeploymentSpec{Replicas: &two},
			Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 2, ReadyReplicas: 2},
		},
	)
	for _, o := range objs {
		require.NoError(t, client.Tracker().Add(o))
	}
	enforcementPodsComplete(client)

	dyn := envoyGatewayClient(t, gatewayObject("nvcf", "shared-gw", "eg"))
	httpRoutes := schema.GroupVersionResource{Group: gatewayAPIGroup, Version: "v1", Resource: "httproutes"}
	require.NoError(t, dyn.Tracker().Create(httpRoutes,
		route("HTTPRoute", "nvcf", "api", "nvcf-gateway-routes-1.18.2", parentRef("name", "shared-gw")), "nvcf"))

	return runValidatorTwice(t, client, &dyn.Fake, func() {
		_ = Run(context.Background(), client, dyn, inventoryNamespace, "cluster-validator-network-checks",
			inventoryNamespace, true, RoleControlPlane)
	})
}

// nodeToNodeActions drives the overlay probe across two nodes, one of them
// still pulling, so the run reads sandbox events too.
func nodeToNodeActions(t *testing.T) []ktesting.Action {
	t.Helper()
	pulling := probePod("node-2", "Pending", "", "ContainerCreating")
	pulling.Name = "s-2"
	f := newN2NFixture(t, []*corev1.Node{makeNode("node-1", true, 0), makeNode("node-2", true, 0)},
		[]corev1.Pod{runningProbePod("s-1", "node-1", "10.0.0.1"), pulling},
		[]corev1.Event{podEvent("s-2", "Pulling", 0)}, checkerExit(0))
	checkNodeToNode(context.Background(), f.client, &ValidationState{Log: testLog()}, enforcementDefaultImg)
	return f.client.Actions()
}

func computePlaneRunActions(t *testing.T) []ktesting.Action {
	t.Helper()
	prev := enforcementPropDelay
	enforcementPropDelay = 0
	t.Cleanup(func() { enforcementPropDelay = prev })

	objs := append(inventoryNamespaces(),
		makeNode("gpu-1", true, 8),
		inventoryNetworkChecks(true),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "gpu-operator"}},
		makePod("gpu-operator-0", "gpu-operator", corev1.PodRunning),
		&storagev1.CSIDriver{ObjectMeta: metav1.ObjectMeta{Name: "smb.csi.k8s.io"}},
	)
	client := fake.NewSimpleClientset(objs...)
	enforcementPodsComplete(client)
	return runValidatorTwice(t, client, nil, func() {
		_ = Run(context.Background(), client, nil, inventoryNamespace, "cluster-validator-network-checks",
			inventoryNamespace, true, RoleComputePlane)
	})
}

// assertInventoryMatches requires every request the role made to be listed,
// and every listed request to have been made, so the inventory is neither
// short (a 403 in production) nor padded (a grant nothing uses).
func assertInventoryMatches(t *testing.T, role Role, actions []ktesting.Action) {
	t.Helper()
	rules := loadRBACInventory(t)
	seen := map[string]bool{}
	var unlisted []string
	for _, req := range requestsOf(actions) {
		seen[req.key()] = true
		if !slices.ContainsFunc(rules, func(r inventoryRule) bool { return r.covers(role, req, inventoryNamespace) }) {
			unlisted = append(unlisted, req.key()+" ns="+req.namespace+" name="+req.name)
		}
	}
	sort.Strings(unlisted)
	assert.Empty(t, slices.Compact(unlisted), "%s requests missing from %s", role, rbacInventoryFile)

	// probeReadyz reads /readyz through the discovery REST client, which the
	// fake clientset does not provide.
	var unused []string
	for _, k := range inventoryKeys(rules, role) {
		if !seen[k] && k != nonResourceGroup+"|/readyz|get" {
			unused = append(unused, k)
		}
	}
	assert.Empty(t, unused, "%s grants in %s that no request made", role, rbacInventoryFile)
}

func TestRBACInventory_ControlPlane(t *testing.T) {
	actions := controlPlaneRunActions(t)
	actions = append(actions, nodeToNodeActions(t)...)
	assertInventoryMatches(t, RoleControlPlane, actions)
}

func TestRBACInventory_ComputePlane(t *testing.T) {
	assertInventoryMatches(t, RoleComputePlane, computePlaneRunActions(t))
}

// The matcher itself: a namespaced grant does not cover another namespace,
// and a named grant does not cover another name.
func TestRBACInventory_CoversScopeAndNames(t *testing.T) {
	rules := loadRBACInventory(t)
	covered := func(role Role, req apiRequest) bool {
		return slices.ContainsFunc(rules, func(r inventoryRule) bool { return r.covers(role, req, inventoryNamespace) })
	}
	update := apiRequest{group: "", resource: "configmaps", verb: "update", namespace: inventoryNamespace,
		name: SummaryConfigMapName}
	assert.True(t, covered(RoleControlPlane, update))
	other := update
	other.name = "kube-proxy"
	assert.False(t, covered(RoleControlPlane, other))
	elsewhere := update
	elsewhere.namespace = "kube-system"
	assert.False(t, covered(RoleControlPlane, elsewhere))
	ds := apiRequest{group: "apps", resource: "daemonsets", verb: "create", namespace: "nvcf-n2n-validation-x"}
	assert.True(t, covered(RoleControlPlane, ds))
	assert.False(t, covered(RoleComputePlane, ds), "the compute-plane set never creates DaemonSets")
}
