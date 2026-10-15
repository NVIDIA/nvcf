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
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// GroupVersionResources for the CRDs we read. Defined upstream in
// nvca/pkg/apis. Resource is the lowercase plural from the CRD spec.names.
var (
	nvcfBackendGVR = schema.GroupVersionResource{
		Group: "nvcf.nvidia.io", Version: "v1", Resource: "nvcfbackends",
	}
	icmsRequestGVR = schema.GroupVersionResource{
		Group: "nvca.nvcf.nvidia.io", Version: "v2beta1", Resource: "icmsrequests",
	}
)

// k8sInspector reads NVCA state from a compute-plane cluster's Kubernetes API
// using the dynamic client. It is the only AgentInspector implementation today.
type k8sInspector struct {
	dc dynamic.Interface
}

// NewK8sInspector returns an AgentInspector backed by the Kubernetes dynamic
// client.
func NewK8sInspector(dc dynamic.Interface) AgentInspector {
	return &k8sInspector{dc: dc}
}

// Status reads the NVCFBackend CR from namespace and maps it to AgentStatus.
func (k *k8sInspector) Status(ctx context.Context, namespace string) (*AgentStatus, error) {
	list, err := k.dc.Resource(nvcfBackendGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, wrapCRDError(err, "NVCFBackend", namespace)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no NVCFBackend resource found in namespace %q; is this context pointed at an NVCF compute-plane cluster (try --namespace)?", namespace)
	}

	obj := list.Items[0].Object
	status := &AgentStatus{
		Namespace:         namespace,
		ClusterID:         firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "clusterId"), nestedString(obj, "spec", "clusterConfig", "clusterID")),
		ClusterName:       nestedString(obj, "spec", "clusterConfig", "clusterName"),
		NVCAVersion:       nestedString(obj, "spec", "version"),
		AgentHealth:       firstNonEmpty(nestedString(obj, "status", "agentStatus"), "Unknown"),
		KubernetesVersion: nestedString(obj, "status", "kubernetesVersion"),
		LastUpdated:       nestedString(obj, "status", "lastUpdated"),
		GPU:               extractGPUUsage(obj),
	}
	return status, nil
}

// ListFunctions lists ICMSRequest CRs and maps them to scheduled functions,
// applying phase filtering and deterministic ordering.
func (k *k8sInspector) ListFunctions(ctx context.Context, opts ListOptions) ([]ScheduledFunction, error) {
	items, err := listICMSRequests(ctx, k.dc, opts.Namespace)
	if err != nil {
		return nil, err
	}

	result := make([]ScheduledFunction, 0, len(items))
	for i := range items {
		fn := scheduledFunctionFromObj(items[i].Object, items[i].GetNamespace())
		if opts.PhaseFilter != "" && fn.Phase != opts.PhaseFilter {
			continue
		}
		result = append(result, fn)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].FunctionID != result[j].FunctionID {
			return result[i].FunctionID < result[j].FunctionID
		}
		if result[i].FunctionVersionID != result[j].FunctionVersionID {
			return result[i].FunctionVersionID < result[j].FunctionVersionID
		}
		return result[i].Namespace < result[j].Namespace
	})
	return result, nil
}

// GetFunction lists ICMSRequest CRs across all namespaces and returns the first
// one matching functionID (and versionID, if set).
func (k *k8sInspector) GetFunction(ctx context.Context, functionID, versionID string) (*FunctionDetail, error) {
	items, err := listICMSRequests(ctx, k.dc, "")
	if err != nil {
		return nil, err
	}

	// Sort before scanning so the "first match" is stable across calls when a
	// functionID has multiple versions (or the same version across namespaces)
	// and the caller omits versionID. Matches the ordering ListFunctions uses.
	sortICMSRequests(items)
	for i := range items {
		obj := items[i].Object
		fid, vid := functionIdentity(obj)
		if fid != functionID {
			continue
		}
		if versionID != "" && vid != versionID {
			continue
		}
		return functionDetailFromObj(obj, items[i].GetNamespace()), nil
	}

	if versionID != "" {
		return nil, fmt.Errorf("no scheduled function found for function %s version %s", functionID, versionID)
	}
	return nil, fmt.Errorf("no scheduled function found for function %s", functionID)
}

// icmsRequestListPageSize bounds each List response so a cluster with many
// ICMSRequest CRs does not force the API server to return everything at once.
const icmsRequestListPageSize = 500

func listICMSRequests(ctx context.Context, dc dynamic.Interface, namespace string) ([]unstructured.Unstructured, error) {
	var items []unstructured.Unstructured
	continueToken := ""
	for {
		opts := metav1.ListOptions{Limit: icmsRequestListPageSize, Continue: continueToken}
		var (
			list *unstructured.UnstructuredList
			err  error
		)
		if namespace == "" {
			list, err = dc.Resource(icmsRequestGVR).List(ctx, opts)
		} else {
			list, err = dc.Resource(icmsRequestGVR).Namespace(namespace).List(ctx, opts)
		}
		if err != nil {
			return nil, wrapCRDError(err, "ICMSRequest", namespace)
		}
		items = append(items, list.Items...)
		continueToken = list.GetContinue()
		if continueToken == "" {
			return items, nil
		}
	}
}

// sortICMSRequests orders items by (functionID, functionVersionID, namespace)
// for a stable scan order.
func sortICMSRequests(items []unstructured.Unstructured) {
	sort.Slice(items, func(i, j int) bool {
		fi, vi := functionIdentity(items[i].Object)
		fj, vj := functionIdentity(items[j].Object)
		if fi != fj {
			return fi < fj
		}
		if vi != vj {
			return vi < vj
		}
		return items[i].GetNamespace() < items[j].GetNamespace()
	})
}

// scheduledFunctionFromObj builds the list-level summary for one ICMSRequest.
func scheduledFunctionFromObj(obj map[string]interface{}, namespace string) ScheduledFunction {
	fid, vid := functionIdentity(obj)
	action := nestedString(obj, "spec", "action")
	requestStatus := nestedString(obj, "status", "requestStatus")
	instances := extractInstances(obj)

	return ScheduledFunction{
		FunctionID:        fid,
		FunctionVersionID: vid,
		Namespace:         namespace,
		Action:            action,
		RequestStatus:     requestStatus,
		Phase:             DerivePhase(requestStatus, action, false, instancesTerminating(instances)),
		InstanceCount:     len(instances),
	}
}

func functionDetailFromObj(obj map[string]interface{}, namespace string) *FunctionDetail {
	return &FunctionDetail{
		ScheduledFunction:  scheduledFunctionFromObj(obj, namespace),
		Instances:          extractInstances(obj),
		LastStatusUpdated:  nestedString(obj, "status", "lastStatusUpdated"),
		LastACKTimestamp:   nestedString(obj, "status", "lastACKTimestamp"),
		LastReconcileError: nestedString(obj, "status", "lastReconcileError"),
		ReconcileErrors:    uint64(nestedInt(obj, "status", "reconcileErrors")),
	}
}

// functionIdentity reads functionId/functionVersionId, preferring the modern
// spec.functionDetails fields and falling back to the deprecated top-level
// spec fields.
func functionIdentity(obj map[string]interface{}) (functionID, versionID string) {
	functionID = firstNonEmpty(
		nestedString(obj, "spec", "functionDetails", "functionId"),
		nestedString(obj, "spec", "functionId"),
	)
	versionID = firstNonEmpty(
		nestedString(obj, "spec", "functionDetails", "functionVersionId"),
		nestedString(obj, "spec", "functionVersionId"),
	)
	return functionID, versionID
}

func extractInstances(obj map[string]interface{}) []Instance {
	raw, found, err := unstructured.NestedMap(obj, "status", "instances")
	if !found || err != nil {
		return nil
	}
	instances := make([]Instance, 0, len(raw))
	for id, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		instances = append(instances, Instance{
			ID:                    firstNonEmpty(nestedString(m, "id"), id),
			Type:                  firstNonEmpty(nestedString(m, "type"), nestedString(m, "instanceType")),
			Status:                nestedString(m, "status"),
			LastReportedStatus:    nestedString(m, "lastReportedStatus"),
			LastReportedTimestamp: nestedString(m, "lastReportedTimestamp"),
			Attributes:            stringMap(m, "attributes"),
		})
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })
	return instances
}

func extractGPUUsage(obj map[string]interface{}) []GPUUsage {
	raw, found, err := unstructured.NestedMap(obj, "status", "gpuUsage")
	if !found || err != nil {
		return nil
	}
	gpus := make([]GPUUsage, 0, len(raw))
	for name, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		gpus = append(gpus, GPUUsage{
			Name:      name,
			Capacity:  nestedInt(m, "capacity"),
			Available: nestedInt(m, "available"),
			Allocated: nestedInt(m, "allocated"),
		})
	}
	sort.Slice(gpus, func(i, j int) bool { return gpus[i].Name < gpus[j].Name })
	return gpus
}

// instancesTerminating reports whether any instance is mid-termination, used as
// a DRAINING signal. Note: cluster-wide CordonAndDrain maintenance mode is a
// stronger signal but is not carried on the ICMSRequest CR; wiring that from
// the NVCFBackend CR is a follow-up once the field is confirmed.
func instancesTerminating(instances []Instance) bool {
	for _, in := range instances {
		if strings.Contains(strings.ToLower(in.Status), "terminat") ||
			strings.Contains(strings.ToLower(in.LastReportedStatus), "terminat") {
			return true
		}
	}
	return false
}

// wrapCRDError turns common dynamic-client failures into actionable messages.
func wrapCRDError(err error, kind, namespace string) error {
	switch {
	case apierrors.IsForbidden(err):
		return fmt.Errorf("not permitted to list %s resources in %s: %w", kind, namespaceLabel(namespace), err)
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%s CRD is not installed on this cluster; is this context pointed at an NVCF compute-plane cluster? %w", kind, err)
	default:
		return fmt.Errorf("failed to list %s resources in %s: %w", kind, namespaceLabel(namespace), err)
	}
}

func namespaceLabel(namespace string) string {
	if namespace == "" {
		return "all namespaces"
	}
	return "namespace " + namespace
}

// --- unstructured field helpers (missing fields degrade to zero values) ---

func nestedString(obj map[string]interface{}, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj, fields...)
	return v
}

func nestedInt(obj map[string]interface{}, fields ...string) int64 {
	v, found, err := unstructured.NestedInt64(obj, fields...)
	if found && err == nil {
		return v
	}
	// gpuUsage/instances counts may decode as float64 from JSON.
	if f, found, err := unstructured.NestedFloat64(obj, fields...); found && err == nil {
		return int64(f)
	}
	return 0
}

func stringMap(obj map[string]interface{}, fields ...string) map[string]string {
	raw, found, err := unstructured.NestedMap(obj, fields...)
	if !found || err != nil || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		} else {
			out[k] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
