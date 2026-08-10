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

package clusterdump

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func icmsListKinds() map[schema.GroupVersionResource]string {
	m := allCoreListKinds()
	m[icmsRequestGVR] = "ICMSRequestList"
	m[nvcfBackendGVR] = "NVCFBackendList"
	for _, gvr := range nvcaStackGVRs {
		// The fake only needs a non-empty list kind per registered GVR.
		m[gvr] = gvr.Resource + "List"
	}
	return m
}

func TestCollectICMSDeep(t *testing.T) {
	container := unstructuredObj("nvca.nvcf.nvidia.io/v2beta1", "ICMSRequest", icmsBackendNamespace, "sr-container", map[string]interface{}{
		"spec":   map[string]interface{}{"functionId": "fn-1", "functionVersionId": "v-1"},
		"status": map[string]interface{}{"requestStatus": "RequestFailed"},
	})
	helm := unstructuredObj("nvca.nvcf.nvidia.io/v2beta1", "ICMSRequest", icmsBackendNamespace, "sr-helm", map[string]interface{}{
		"spec": map[string]interface{}{
			"functionId": "fn-2",
			"creationMsgInfo": map[string]interface{}{
				"functionLaunchSpecification": map[string]interface{}{"helmChart": "oci://chart"},
			},
		},
		"status": map[string]interface{}{"requestStatus": "RequestCompleted"},
	})
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), icmsListKinds(), container, helm)
	pc := &PlaneCollector{
		Role:       RoleComputePlane,
		Kube:       k8sfake.NewSimpleClientset(),
		Dynamic:    dc,
		Namespaces: ComputePlaneNamespaces(),
		Options:    DumpOptions{Sections: map[string]bool{SectionResources: true}},
	}

	summaries, crs, warns := pc.collectICMSSummaries(context.Background())
	assert.Empty(t, warns)
	require.Len(t, summaries, 2)

	byName := map[string]ICMSRequestSummary{}
	for _, s := range summaries {
		byName[s.Name] = s
	}
	assert.Equal(t, "container", byName["sr-container"].Type)
	assert.True(t, byName["sr-container"].Failed)
	assert.Equal(t, "fn-1", byName["sr-container"].FunctionID)
	assert.Equal(t, "helm", byName["sr-helm"].Type)
	assert.False(t, byName["sr-helm"].Failed)
	assert.Equal(t, "sr-helm", byName["sr-helm"].Namespace) // helm function namespace == request name

	// Both ICMSRequest CRs are captured as YAML.
	var icmsCaptures int
	for _, r := range crs {
		if r.Resource == "icmsrequests" {
			icmsCaptures++
		}
	}
	assert.Equal(t, 2, icmsCaptures)
	assert.Len(t, failedICMS(summaries), 1)

	// Only the helm function contributes a request-named namespace for the
	// (bundle-only) deep capture.
	assert.Equal(t, []string{"sr-helm"}, helmFunctionNamespaces(summaries))
}

func TestCollectGPUReconciliation(t *testing.T) {
	node := func(name, clique string, gpus string) *corev1.Node {
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{gpuCliqueLabel: clique}},
			Status:     corev1.NodeStatus{Capacity: corev1.ResourceList{gpuResourceName: resource.MustParse(gpus)}},
		}
	}
	kube := k8sfake.NewSimpleClientset(node("n1", "c1", "4"), node("n2", "c1", "4"))

	backend := unstructuredObj("nvcf.nvidia.io/v1", "NVCFBackend", "nvca-operator", "be", map[string]interface{}{
		"status": map[string]interface{}{
			"gpuUsage": map[string]interface{}{
				"A100": map[string]interface{}{"capacity": int64(8), "allocated": int64(0), "available": int64(8)},
			},
		},
	})
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), icmsListKinds(), backend)

	pc := &PlaneCollector{Role: RoleComputePlane, Kube: kube, Dynamic: dc}
	rec, warns := pc.collectGPUReconciliation(context.Background())
	assert.Empty(t, warns)
	require.NotNil(t, rec)
	assert.Equal(t, int64(8), rec.NodeCapacity)
	assert.Equal(t, int64(8), rec.BackendCapacity)
	assert.Equal(t, int64(0), rec.NodeAllocatedByPods)
	assert.True(t, rec.Match)
	require.Len(t, rec.Cliques, 1)
	assert.Equal(t, "c1", rec.Cliques[0].Clique)
	assert.Equal(t, 2, rec.Cliques[0].Nodes)
	assert.Equal(t, int64(8), rec.Cliques[0].Capacity)
}

// TestCollectICMSDeep_NoResourcesSection verifies the always-on triage summary
// is still produced, but the raw ICMSRequest YAML is only captured when the
// operator selected the resources section (e.g. not under --include logs only).
func TestCollectICMSDeep_NoResourcesSection(t *testing.T) {
	container := unstructuredObj("nvca.nvcf.nvidia.io/v2beta1", "ICMSRequest", icmsBackendNamespace, "sr-container", map[string]interface{}{
		"spec":   map[string]interface{}{"functionId": "fn-1"},
		"status": map[string]interface{}{"requestStatus": "RequestCompleted"},
	})
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), icmsListKinds(), container)
	pc := &PlaneCollector{
		Role:       RoleComputePlane,
		Kube:       k8sfake.NewSimpleClientset(),
		Dynamic:    dc,
		Namespaces: ComputePlaneNamespaces(),
		Options:    DumpOptions{Sections: map[string]bool{SectionLogs: true}},
	}

	summaries, crs, warns := pc.collectICMSSummaries(context.Background())
	assert.Empty(t, warns)
	require.Len(t, summaries, 1) // triage summary is always collected
	assert.Empty(t, crs, "raw ICMSRequest YAML must not be captured without the resources section")
}

// TestCollectGPUReconciliation_UnscheduledPods verifies GPUs requested by pods
// not yet bound to a node are excluded from per-node allocation and surfaced as
// a note rather than silently dropped into byNode[""].
func TestCollectGPUReconciliation_UnscheduledPods(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{gpuCliqueLabel: "c1"}},
		Status:     corev1.NodeStatus{Capacity: corev1.ResourceList{gpuResourceName: resource.MustParse("8")}},
	}
	gpuPod := func(name, nodeName string, phase corev1.PodPhase, gpus string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName: nodeName,
				Containers: []corev1.Container{{
					Name: "c",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{gpuResourceName: resource.MustParse(gpus)},
					},
				}},
			},
			Status: corev1.PodStatus{Phase: phase},
		}
	}
	kube := k8sfake.NewSimpleClientset(
		node,
		gpuPod("scheduled", "n1", corev1.PodRunning, "2"),
		gpuPod("pending", "", corev1.PodPending, "4"),
	)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), icmsListKinds())
	pc := &PlaneCollector{Role: RoleComputePlane, Kube: kube, Dynamic: dc}

	rec, warns := pc.collectGPUReconciliation(context.Background())
	assert.Empty(t, warns)
	require.NotNil(t, rec)
	assert.Equal(t, int64(2), rec.NodeAllocatedByPods, "only the scheduled pod's GPUs count toward node allocation")

	var noted bool
	for _, n := range rec.Notes {
		if strings.Contains(n, "pending/unscheduled") {
			noted = true
		}
	}
	assert.True(t, noted, "expected a note about unscheduled GPU pods; notes=%v", rec.Notes)
}

func TestRedactValues(t *testing.T) {
	in := map[string]interface{}{
		"name":              "ok",
		"password":          "hunter2-very-long-secret-value",
		"authorizationMode": "RBAC", // must NOT be masked (substring "auth")
		"nested": map[string]interface{}{
			"apiToken": "tok-abcdefghijklmnop",
			"plain":    "fine",
		},
	}
	out := redactValues(in, DumpOptions{Redact: RedactSecrets})
	assert.Equal(t, "ok", out["name"])
	assert.NotEqual(t, "hunter2-very-long-secret-value", out["password"])
	assert.Contains(t, out["password"].(string), "redacted")
	assert.Equal(t, "RBAC", out["authorizationMode"], "whole-word auth anchoring must not over-redact")
	nested := out["nested"].(map[string]interface{})
	assert.NotEqual(t, "tok-abcdefghijklmnop", nested["apiToken"])
	assert.Equal(t, "fine", nested["plain"])

	// redact=none leaves values intact.
	none := redactValues(in, DumpOptions{Redact: RedactNone})
	assert.Equal(t, "hunter2-very-long-secret-value", none["password"])
}

func TestRedactManifest(t *testing.T) {
	manifest := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ndata:\n  token: bG9uZ3NlY3JldHZhbHVlMTIzNDU2Nzg5MGFiYw==\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  key: keepme\n"
	out := redactManifest(manifest, DumpOptions{Redact: RedactSecrets})
	assert.NotContains(t, out, "bG9uZ3NlY3JldHZhbHVlMTIzNDU2Nzg5MGFiYw==")
	assert.Contains(t, out, "redacted")
	assert.Contains(t, out, "keepme") // ConfigMap data preserved under default redaction
}

// TestRedactManifest_EmbeddedSeparator verifies a "---" line inside a Secret
// value does not mis-split the document and let secret data through unredacted,
// and that the following document is still captured intact.
func TestRedactManifest_EmbeddedSeparator(t *testing.T) {
	manifest := "" +
		"apiVersion: v1\n" +
		"kind: Secret\n" +
		"metadata:\n  name: s\n" +
		"data:\n  token: bG9uZ3NlY3JldHZhbHVlMTIzNDU2Nzg5MGFiYw==\n" +
		"stringData:\n  config: |\n    foo: bar\n    ---\n    baz: qux\n" +
		"---\n" +
		"apiVersion: v1\n" +
		"kind: ConfigMap\n" +
		"metadata:\n  name: c\n" +
		"data:\n  key: keepme\n"

	out := redactManifest(manifest, DumpOptions{Redact: RedactSecrets})
	assert.NotContains(t, out, "bG9uZ3NlY3JldHZhbHVlMTIzNDU2Nzg5MGFiYw==", "secret data masked")
	assert.Contains(t, out, "redacted")
	assert.Contains(t, out, "keepme", "trailing ConfigMap document preserved, not lost to a mis-split")
}

func TestCollectStuckNamespaces(t *testing.T) {
	term := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "sr-stuck"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	}
	active := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	kube := k8sfake.NewSimpleClientset(term, active)

	blocker := unstructuredObj("v1", "ConfigMap", "sr-stuck", "blocker", nil)
	blocker.SetFinalizers([]string{"nvca.nvcf.nvidia.io/cleanup"})
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), icmsListKinds(), blocker)

	pc := &PlaneCollector{
		Role:    RoleComputePlane,
		Kube:    kube,
		Dynamic: dc,
	}
	out, warns := pc.collectStuckNamespaces(context.Background())
	assert.Empty(t, warns)
	require.Len(t, out, 1) // only the Terminating namespace
	assert.Equal(t, "sr-stuck", out[0].Name)
	require.Len(t, out[0].RemainingObjects, 1)
	assert.Equal(t, "ConfigMap", out[0].RemainingObjects[0].Kind)
	assert.Contains(t, out[0].RemainingObjects[0].Finalizers, "nvca.nvcf.nvidia.io/cleanup")

	rep := renderStuckNamespace(out[0])
	assert.Contains(t, rep, "blocker")
	assert.Contains(t, rep, "NOT performed") // remedy is documented, not executed

	// Read-only guarantee: every dynamic-client action is a list.
	for _, a := range dc.Actions() {
		assert.Equal(t, "list", a.GetVerb())
	}
}
