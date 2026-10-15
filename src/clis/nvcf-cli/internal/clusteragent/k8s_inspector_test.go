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

package clusteragent

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newFakeInspector(objs ...runtime.Object) AgentInspector {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		nvcfBackendGVR: "NVCFBackendList",
		icmsRequestGVR: "ICMSRequestList",
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	return NewK8sInspector(dc)
}

func nvcfBackend(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvcf.nvidia.io/v1",
		"kind":       "NVCFBackend",
		"metadata":   map[string]interface{}{"namespace": namespace, "name": name},
		"spec": map[string]interface{}{
			"version": "2.30.4",
			"clusterConfig": map[string]interface{}{
				"clusterId":   "cluster-uuid-1",
				"clusterName": "edge-1",
			},
		},
		"status": map[string]interface{}{
			"agentStatus":       "Healthy",
			"kubernetesVersion": "v1.30.2",
			"lastUpdated":       "2026-05-30T10:00:00Z",
			"gpuUsage": map[string]interface{}{
				"A100": map[string]interface{}{
					"capacity":  int64(8),
					"available": int64(3),
					"allocated": int64(5),
				},
			},
		},
	}}
}

func icmsRequest(namespace, name, functionID, versionID, action, status string, useLegacy bool) *unstructured.Unstructured {
	spec := map[string]interface{}{"action": action}
	if useLegacy {
		spec["functionId"] = functionID
		spec["functionVersionId"] = versionID
	} else {
		spec["functionDetails"] = map[string]interface{}{
			"functionId":        functionID,
			"functionVersionId": versionID,
		}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
		"kind":       "ICMSRequest",
		"metadata":   map[string]interface{}{"namespace": namespace, "name": name},
		"spec":       spec,
		"status": map[string]interface{}{
			"requestStatus": status,
			"instances": map[string]interface{}{
				"inst-a": map[string]interface{}{
					"id":     "inst-a",
					"type":   "Pod",
					"status": "Running",
				},
			},
		},
	}}
}

func TestStatusExtractsBackendFields(t *testing.T) {
	insp := newFakeInspector(nvcfBackend("nvca-operator", "backend"))

	st, err := insp.Status(context.Background(), "nvca-operator")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if st.NVCAVersion != "2.30.4" {
		t.Errorf("NVCAVersion = %q, want 2.30.4", st.NVCAVersion)
	}
	if st.AgentHealth != "Healthy" {
		t.Errorf("AgentHealth = %q, want Healthy", st.AgentHealth)
	}
	if st.ClusterID != "cluster-uuid-1" || st.ClusterName != "edge-1" {
		t.Errorf("cluster identity = %q/%q, want cluster-uuid-1/edge-1", st.ClusterID, st.ClusterName)
	}
	if len(st.GPU) != 1 || st.GPU[0].Name != "A100" || st.GPU[0].Capacity != 8 || st.GPU[0].Allocated != 5 {
		t.Errorf("GPU = %+v, want one A100 with capacity 8 allocated 5", st.GPU)
	}
}

func TestStatusErrorsWhenNoBackend(t *testing.T) {
	insp := newFakeInspector()
	if _, err := insp.Status(context.Background(), "nvca-operator"); err == nil {
		t.Fatal("expected error when no NVCFBackend exists")
	}
}

func TestListFunctionsAcrossNamespaces(t *testing.T) {
	insp := newFakeInspector(
		icmsRequest("ns-b", "r2", "fn-2", "v1", "", statusCompleted, false),
		icmsRequest("ns-a", "r1", "fn-1", "v1", "", statusInProgress, true),
		icmsRequest("ns-a", "r3", "fn-1", "v2", "", statusFailed, false),
	)

	got, err := insp.ListFunctions(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListFunctions returned error: %v", err)
	}
	// FAILED functions are included by default; deterministic order fn-1 then fn-2.
	if len(got) != 3 {
		t.Fatalf("got %d functions, want 3 (failed included): %+v", len(got), got)
	}
	if got[0].FunctionID != "fn-1" || got[1].FunctionID != "fn-1" || got[2].FunctionID != "fn-2" {
		t.Errorf("order = %s,%s,%s want fn-1,fn-1,fn-2", got[0].FunctionID, got[1].FunctionID, got[2].FunctionID)
	}
	if got[0].Phase != PhaseDeploying {
		t.Errorf("fn-1 phase = %q, want DEPLOYING", got[0].Phase)
	}
	if got[0].InstanceCount != 1 {
		t.Errorf("fn-1 instance count = %d, want 1", got[0].InstanceCount)
	}
}

func TestListFunctionsFailedPhaseFilter(t *testing.T) {
	insp := newFakeInspector(
		icmsRequest("ns-a", "r1", "fn-1", "v1", "", statusCompleted, false),
		icmsRequest("ns-a", "r2", "fn-2", "v1", "", statusFailed, false),
	)

	got, err := insp.ListFunctions(context.Background(), ListOptions{PhaseFilter: PhaseFailed})
	if err != nil {
		t.Fatalf("ListFunctions returned error: %v", err)
	}
	if len(got) != 1 || got[0].FunctionID != "fn-2" {
		t.Fatalf("got %+v, want only fn-2 with --phase FAILED", got)
	}
}

func TestListFunctionsPhaseFilter(t *testing.T) {
	insp := newFakeInspector(
		icmsRequest("ns-a", "r1", "fn-1", "v1", "", statusCompleted, false),
		icmsRequest("ns-a", "r2", "fn-2", "v1", "", statusInProgress, false),
		icmsRequest("ns-a", "r3", "fn-3", "v1", actionTermination, statusCompleted, false),
	)

	got, err := insp.ListFunctions(context.Background(), ListOptions{PhaseFilter: PhaseDraining})
	if err != nil {
		t.Fatalf("ListFunctions returned error: %v", err)
	}
	if len(got) != 1 || got[0].FunctionID != "fn-3" {
		t.Fatalf("phase filter DRAINING = %+v, want only fn-3", got)
	}
}

func TestGetFunctionMatchesVersion(t *testing.T) {
	insp := newFakeInspector(
		icmsRequest("ns-a", "r1", "fn-1", "v1", "", statusCompleted, false),
		icmsRequest("ns-a", "r2", "fn-1", "v2", "", statusInProgress, false),
	)

	d, err := insp.GetFunction(context.Background(), "fn-1", "v2")
	if err != nil {
		t.Fatalf("GetFunction returned error: %v", err)
	}
	if d.FunctionVersionID != "v2" || d.Phase != PhaseDeploying {
		t.Errorf("got version %q phase %q, want v2 DEPLOYING", d.FunctionVersionID, d.Phase)
	}
	if len(d.Instances) != 1 || d.Instances[0].ID != "inst-a" {
		t.Errorf("instances = %+v, want one inst-a", d.Instances)
	}
}

func TestGetFunctionDeterministicWithoutVersion(t *testing.T) {
	// Same functionID, two versions in different namespaces. With versionID
	// omitted, the result must be stable (lowest version by sort order).
	insp := newFakeInspector(
		icmsRequest("ns-b", "r2", "fn-x", "v2", "", statusCompleted, false),
		icmsRequest("ns-a", "r1", "fn-x", "v1", "", statusInProgress, false),
	)

	d, err := insp.GetFunction(context.Background(), "fn-x", "")
	if err != nil {
		t.Fatalf("GetFunction returned error: %v", err)
	}
	if d.FunctionVersionID != "v1" {
		t.Errorf("GetFunction(fn-x, \"\") = version %q, want deterministic v1", d.FunctionVersionID)
	}
}

func TestGetFunctionNotFound(t *testing.T) {
	insp := newFakeInspector(icmsRequest("ns-a", "r1", "fn-1", "v1", "", statusCompleted, false))
	if _, err := insp.GetFunction(context.Background(), "missing", ""); err == nil {
		t.Fatal("expected error for missing function")
	}
}
