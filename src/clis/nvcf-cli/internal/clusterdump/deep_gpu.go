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
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// gpuCliqueLabel groups GPU nodes that share an interconnect domain.
const gpuCliqueLabel = "nvidia.com/gpu.clique"

// CliqueGPU summarises GPU capacity/allocation for one clique of nodes.
type CliqueGPU struct {
	Clique    string `json:"clique"`
	Nodes     int    `json:"nodes"`
	Capacity  int64  `json:"capacity"`
	Allocated int64  `json:"allocated"` // from pod nvidia.com/gpu requests
	Available int64  `json:"available"`
}

// GPUReconciliation compares the GPU view the NVCFBackend reports against what
// Kubernetes nodes actually expose, the classic source of "why won't my
// function schedule" confusion on a self-managed cluster.
type GPUReconciliation struct {
	NodeCapacity        int64       `json:"nodeCapacity"`
	NodeAllocatedByPods int64       `json:"nodeAllocatedByPods"`
	MiniServiceReserved int64       `json:"miniServiceReserved"`
	BackendCapacity     int64       `json:"backendCapacity"`
	BackendAllocated    int64       `json:"backendAllocated"`
	BackendAvailable    int64       `json:"backendAvailable"`
	Cliques             []CliqueGPU `json:"cliques,omitempty"`
	Match               bool        `json:"match"`
	Notes               []string    `json:"notes,omitempty"`
}

// collectGPUReconciliation builds the GPU reconciliation for the compute plane.
func (pc *PlaneCollector) collectGPUReconciliation(ctx context.Context) (*GPUReconciliation, []string) {
	var warns []string
	rec := &GPUReconciliation{}

	// Node capacity grouped by clique, and pod-requested allocation per node.
	nodes, err := pc.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		warns = append(warns, fmt.Sprintf("gpu nodes: %v", err))
	} else {
		allocByNode, unscheduledGPU := pc.gpuRequestsByNode(ctx, &warns)
		if unscheduledGPU > 0 {
			rec.Notes = append(rec.Notes, fmt.Sprintf(
				"%d GPU(s) requested by pending/unscheduled pods (no node assigned yet); excluded from node pod-requests",
				unscheduledGPU))
		}
		cliques := map[string]*CliqueGPU{}
		for i := range nodes.Items {
			n := &nodes.Items[i]
			q, ok := n.Status.Capacity[gpuResourceName]
			if !ok {
				continue
			}
			cap := q.Value()
			clique := n.Labels[gpuCliqueLabel]
			if clique == "" {
				clique = "(none)"
			}
			c := cliques[clique]
			if c == nil {
				c = &CliqueGPU{Clique: clique}
				cliques[clique] = c
			}
			c.Nodes++
			c.Capacity += cap
			c.Allocated += allocByNode[n.Name]
			rec.NodeCapacity += cap
			rec.NodeAllocatedByPods += allocByNode[n.Name]
		}
		for _, c := range cliques {
			c.Available = c.Capacity - c.Allocated
			rec.Cliques = append(rec.Cliques, *c)
		}
		sort.Slice(rec.Cliques, func(i, j int) bool { return rec.Cliques[i].Clique < rec.Cliques[j].Clique })
	}

	// NVCFBackend-reported GPU totals.
	// Cluster-wide, matching collectNVCFBackends, so the GPU totals and the
	// NVCFBackend table never disagree on a multi-namespace deployment.
	if backends, err := pc.listResource(ctx, nvcfBackendGVR, ""); err != nil {
		warns = append(warns, fmt.Sprintf("gpu nvcfbackend: %v", err))
	} else {
		for i := range backends {
			cap, alloc, avail := backendGPUTotals(backends[i].Object)
			rec.BackendCapacity += cap
			rec.BackendAllocated += alloc
			rec.BackendAvailable += avail
		}
	}

	// MiniService (helm function) GPU reservations from ICMSRequests.
	if reqs, err := pc.listResource(ctx, icmsRequestGVR, icmsBackendNamespace); err == nil {
		rec.MiniServiceReserved = miniServiceReservedGPU(reqs)
	}

	effectiveAllocated := rec.NodeAllocatedByPods + rec.MiniServiceReserved
	rec.Match = rec.BackendCapacity == rec.NodeCapacity && rec.BackendAllocated == effectiveAllocated
	if !rec.Match {
		rec.Notes = append(rec.Notes, fmt.Sprintf(
			"NVCFBackend allocated=%d vs node pod-requests=%d + miniservice-reservations=%d = %d",
			rec.BackendAllocated, rec.NodeAllocatedByPods, rec.MiniServiceReserved, effectiveAllocated))
	}
	return rec, warns
}

// gpuRequestsByNode sums nvidia.com/gpu container requests per node from
// non-terminal pods across all namespaces. It also returns the total GPUs
// requested by pods that are not yet bound to a node (no NodeName, e.g. Pending
// and unschedulable), which belong to no node's allocation and would otherwise
// be silently dropped into byNode[""].
func (pc *PlaneCollector) gpuRequestsByNode(ctx context.Context, warns *[]string) (map[string]int64, int64) {
	byNode := map[string]int64{}
	var unscheduled int64
	cont := ""
	for {
		// Cluster-wide pod list, paginated so a busy cluster does not force the
		// API server to return everything in one (slow, memory-heavy) response.
		pods, err := pc.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: stackListPageSize, Continue: cont})
		if err != nil {
			*warns = append(*warns, fmt.Sprintf("gpu pods: %v", err))
			return byNode, unscheduled
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			switch p.Status.Phase {
			case "Failed", "Succeeded":
				continue
			}
			var sum int64
			for _, c := range p.Spec.Containers {
				if q, ok := c.Resources.Requests[gpuResourceName]; ok {
					sum += q.Value()
				}
			}
			if sum > 0 {
				if p.Spec.NodeName == "" {
					// Not yet scheduled: counts against no node's allocation.
					unscheduled += sum
					continue
				}
				byNode[p.Spec.NodeName] += sum
			}
		}
		cont = pods.Continue
		if cont == "" {
			return byNode, unscheduled
		}
	}
}

// backendGPUTotals sums capacity/allocated/available across the NVCFBackend
// status.gpuUsage entries.
func backendGPUTotals(obj map[string]interface{}) (capacity, allocated, available int64) {
	raw, found, err := unstructured.NestedMap(obj, "status", "gpuUsage")
	if !found || err != nil {
		return 0, 0, 0
	}
	for _, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		capacity += nestedInt64(m, "capacity")
		allocated += nestedInt64(m, "allocated")
		available += nestedInt64(m, "available")
	}
	return capacity, allocated, available
}

// miniServiceReservedGPU sums GPU reservations across MiniService instances of
// all ICMSRequests.
func miniServiceReservedGPU(reqs []unstructured.Unstructured) int64 {
	var total int64
	for i := range reqs {
		obj := reqs[i].Object
		instances, found, err := unstructured.NestedMap(obj, "status", "instances")
		if !found || err != nil {
			continue
		}
		isMini := false
		for _, v := range instances {
			if m, ok := v.(map[string]interface{}); ok {
				if nestedStr(m, "instanceType") == "MiniService" || nestedStr(m, "type") == "MiniService" {
					isMini = true
					break
				}
			}
		}
		if !isMini {
			continue
		}
		count := nestedInt64(obj, "spec", "creationMsgInfo", "requestedGPUCount")
		// nestedInt64 returns 0 for both an explicit 0 and a missing field. Only
		// default to 1 when the field is genuinely absent (older CRDs that imply
		// a single GPU); a legitimate CPU-only MiniService keeps its 0.
		if _, found, _ := unstructured.NestedFieldNoCopy(obj, "spec", "creationMsgInfo", "requestedGPUCount"); !found {
			count = 1
		}
		total += count
	}
	return total
}
