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

package nvcfdra

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	RequiredNVLinkDomainIndexAnnotation = draFQDNprefix + "/required-nvlink-domain-index"
	NVLinkDomainPartitionLabel          = draFQDNprefix + "/nvlink-domain-partition"
	GPUCliqueNodeLabel                  = "nvidia.com/gpu.clique"

	GPUDeviceClassName = "gpu.nvidia.com"

	draFQDNprefix = "dra.nvcf.nvidia.io"
)

func TransformNVLinkOptimizedDRAObjects(
	sourceObjs []client.Object,
	keyToHash string,
	minGPUs int64,
) (retObjs, draObjs []client.Object, err error) {
	if keyToHash == "" {
		return nil, nil, fmt.Errorf("key to partition NVLink domains is empty")
	}
	// Sanitize indices by converting them to integers.
	reqNVDIndexMap, err := sanitizeIndices(sourceObjs)
	if err != nil {
		return nil, nil, err
	}

	prefNVDObjs := []client.Object{}
	objsByReqNVDIndex := map[int][]client.Object{}
	for _, sourceObj := range sourceObjs {
		annos := sourceObj.GetAnnotations()
		if annos == nil {
			prefNVDObjs = append(prefNVDObjs, sourceObj)
			continue
		}
		if idxStr, ok := annos[RequiredNVLinkDomainIndexAnnotation]; ok {
			idx := reqNVDIndexMap[idxStr]
			objsByReqNVDIndex[idx] = append(objsByReqNVDIndex[idx], sourceObj)
		} else {
			prefNVDObjs = append(prefNVDObjs, sourceObj)
		}
	}

	cd := NewSingleChannelComputeDomain()
	SetComputeDomainToGPUPodResourceClaims(cd, minGPUs, sourceObjs...)

	SetPreferredNVLinkDomainSchedulingParameters(keyToHash, prefNVDObjs...)
	for idx, objs := range objsByReqNVDIndex {
		SetRequiredNVLinkDomainSchedulingParameters(keyToHash, fmt.Sprint(idx), objs...)
	}

	return sourceObjs, []client.Object{cd}, nil
}

func sanitizeIndices(objs []client.Object) (map[string]int, error) {
	const internalIndex = 0
	// Sanitize indices by converting them to integers.
	type strIndexTuple struct {
		i int
		s string
	}
	var indexTuples []strIndexTuple
	indexSet := sets.New[string]()
	for _, sourceObj := range objs {
		annos := sourceObj.GetAnnotations()
		if annos == nil {
			continue
		}
		if idx, ok := annos[RequiredNVLinkDomainIndexAnnotation]; ok {
			if indexSet.Has(idx) {
				continue
			}
			indexSet.Insert(idx)
			i, err := strconv.ParseInt(idx, 10, 32)
			if err != nil {
				return nil, err
			}
			indexTuples = append(indexTuples, strIndexTuple{
				i: int(i),
				s: idx,
			})
		}
	}
	if len(indexTuples) == 0 {
		indexTuples = append(indexTuples, strIndexTuple{
			i: internalIndex,
			s: fmt.Sprint(internalIndex),
		})
	} else {
		sort.Slice(indexTuples, func(i, j int) bool {
			return indexTuples[i].i < indexTuples[j].i
		})
		for i := range indexTuples {
			indexTuples[i].i = i + 1
		}
	}
	indexStringToInt := make(map[string]int, len(indexTuples))
	for _, tuple := range indexTuples {
		indexStringToInt[tuple.s] = tuple.i
	}
	return indexStringToInt, nil
}

const (
	defaultComputeDomainName        = "nvcf-cd-index-0"
	defaultComputeDomainChannelName = "nvcf-cd-channel-0"
)

// ComputeDomainChannelPolicy reports whether workloads running on
// instanceTypeName need ComputeDomain IMEX channels at all and, when they do, the
// whole-GPU request a container must make to be given one.
//
// An IMEX channel exists so that GPUs in *different* nodes can share memory over
// NVLink. Only a multi-node instance type ("<base>_<N>x.x<M>", M > 1) spans nodes,
// and inside one only a container that owns a whole node (N GPUs) is part of the
// multi-node gang. A sub-node worker fits within a single node and has nothing to
// share across one.
//
// Handing a channel to a sub-node worker is actively harmful rather than merely
// redundant: every node advertises exactly one channel device and the
// compute-domain-default-channel device class pins it to id 0, so the claim makes
// the worker the node's exclusive GPU tenant and strands the remaining GPUs.
//
// A name with no "_Nx" multiplier is not recognised, and returns (0, true) to
// preserve the historical behaviour of claiming a channel for every GPU container.
func ComputeDomainChannelPolicy(instanceTypeName string) (minGPUs int64, needed bool) {
	name := nvcatypes.InstanceName(instanceTypeName)
	gpusPerNode := name.GetGPUMultiplier()
	if gpusPerNode == 0 {
		return 0, true
	}
	if name.GetNodeCount() <= 1 {
		return 0, false
	}
	return int64(gpusPerNode), true
}

func NewSingleChannelComputeDomain() *nvresourcev1beta1.ComputeDomain {
	cd := &nvresourcev1beta1.ComputeDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultComputeDomainName,
		},
		Spec: nvresourcev1beta1.ComputeDomainSpec{
			Channel: &nvresourcev1beta1.ComputeDomainChannelSpec{
				ResourceClaimTemplate: nvresourcev1beta1.ComputeDomainResourceClaimTemplate{
					Name: defaultComputeDomainChannelName,
				},
			},
		},
	}
	return cd
}

// SetComputeDomainToGPUPodResourceClaims attaches cd's channel claim to the GPU
// containers of objs. minGPUs is the whole-GPU capacity of one node, as returned
// by ComputeDomainChannelPolicy; containers requesting less than that are skipped
// because they cannot take part in multi-node NVLink. A minGPUs of 0 or 1 claims a
// channel for every GPU container.
func SetComputeDomainToGPUPodResourceClaims(
	cd *nvresourcev1beta1.ComputeDomain,
	minGPUs int64,
	objs ...client.Object,
) {
	mf := func(pts *corev1.PodTemplateSpec) {
		ps := &pts.Spec
		anyUpdated := false
		for ci, c := range append(ps.Containers, ps.InitContainers...) {
			if containerNeedsComputeDomainChannel(c, minGPUs) {
				anyUpdated = true
				c.Resources.Claims = append(c.Resources.Claims, corev1.ResourceClaim{
					Name: cd.Name,
				})
				if cl := len(ps.Containers); ci < cl {
					ps.Containers[ci] = c
				} else {
					ps.InitContainers[ci-cl] = c
				}
			}
		}
		if anyUpdated {
			ps.ResourceClaims = append(ps.ResourceClaims, corev1.PodResourceClaim{
				Name:                      cd.Name,
				ResourceClaimTemplateName: &cd.Spec.Channel.ResourceClaimTemplate.Name,
			})
		}
	}
	iterPodSpecs(mf, objs...)
}

func SetPreferredNVLinkDomainSchedulingParameters(keyToHash string, objs ...client.Object) {
	nvlinkDomainPartitionLabelVal := newPartitionKey([]byte(keyToHash))

	podAffinityTerm := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      NVLinkDomainPartitionLabel,
					Operator: metav1.LabelSelectorOpExists,
				},
				{
					Key:      NVLinkDomainPartitionLabel,
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{nvlinkDomainPartitionLabelVal},
				},
			},
		},
		TopologyKey: GPUCliqueNodeLabel,
	}
	cliqueNodeSelTerm := corev1.NodeSelectorTerm{
		MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key:      GPUCliqueNodeLabel,
			Operator: corev1.NodeSelectorOpExists,
		}},
	}

	itrf := func(pts *corev1.PodTemplateSpec) {
		ps := &pts.Spec
		if pts.Labels == nil {
			pts.Labels = map[string]string{}
		}
		pts.Labels[NVLinkDomainPartitionLabel] = nvlinkDomainPartitionLabelVal
		if ps.Affinity == nil {
			ps.Affinity = &corev1.Affinity{}
		}
		if ps.Affinity.PodAffinity == nil {
			ps.Affinity.PodAffinity = &corev1.PodAffinity{}
		}
		ps.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
			ps.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
			corev1.WeightedPodAffinityTerm{
				Weight:          100,
				PodAffinityTerm: podAffinityTerm,
			},
		)
		if ps.Affinity.NodeAffinity == nil {
			ps.Affinity.NodeAffinity = &corev1.NodeAffinity{}
		}
		if ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
		}
		ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
			ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
			cliqueNodeSelTerm,
		)
	}
	iterPodSpecs(itrf, objs...)
}

func SetRequiredNVLinkDomainSchedulingParameters(
	keyToHash, idxStr string,
	objs ...client.Object,
) {
	nvlinkDomainPartitionLabelVal := newPartitionKey(append([]byte(keyToHash), []byte(idxStr)...))

	podAffinityTerm := corev1.PodAffinityTerm{
		LabelSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      NVLinkDomainPartitionLabel,
					Operator: metav1.LabelSelectorOpExists,
				},
				{
					Key:      NVLinkDomainPartitionLabel,
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{nvlinkDomainPartitionLabelVal},
				},
			},
		},
		TopologyKey: GPUCliqueNodeLabel,
	}
	cliqueNodeSelTerm := corev1.NodeSelectorTerm{
		MatchExpressions: []corev1.NodeSelectorRequirement{{
			Key:      GPUCliqueNodeLabel,
			Operator: corev1.NodeSelectorOpExists,
		}},
	}

	itrf := func(pts *corev1.PodTemplateSpec) {
		ps := &pts.Spec
		if pts.Labels == nil {
			pts.Labels = map[string]string{}
		}
		pts.Labels[NVLinkDomainPartitionLabel] = nvlinkDomainPartitionLabelVal
		if ps.Affinity == nil {
			ps.Affinity = &corev1.Affinity{}
		}
		if ps.Affinity.PodAffinity == nil {
			ps.Affinity.PodAffinity = &corev1.PodAffinity{}
		}
		ps.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution = append(
			ps.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution,
			podAffinityTerm,
		)
		if ps.Affinity.NodeAffinity == nil {
			ps.Affinity.NodeAffinity = &corev1.NodeAffinity{}
		}
		if ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
		}
		ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
			ps.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
			cliqueNodeSelTerm,
		)
	}

	iterPodSpecs(itrf, objs...)
}

type iterPodTemplateSpecFunc func(*corev1.PodTemplateSpec)

func iterPodSpecs(itrf iterPodTemplateSpecFunc, objs ...client.Object) {
	for _, obj := range objs {
		switch ot := obj.(type) {
		case *corev1.Pod:
			pts := &corev1.PodTemplateSpec{
				ObjectMeta: ot.ObjectMeta,
				Spec:       ot.Spec,
			}
			itrf(pts)
			ot.ObjectMeta = pts.ObjectMeta
			ot.Spec = pts.Spec
		case *appsv1.Deployment:
			itrf(&ot.Spec.Template)
		case *appsv1.ReplicaSet:
			itrf(&ot.Spec.Template)
		case *appsv1.StatefulSet:
			itrf(&ot.Spec.Template)
		case *batchv1.Job:
			itrf(&ot.Spec.Template)
		case *batchv1.CronJob:
			itrf(&ot.Spec.JobTemplate.Spec.Template)
		default:
			continue
		}
	}
}

var (
	gpuResourceKeys = []corev1.ResourceName{
		corev1.ResourceName("nvidia.com/gpu"),
		corev1.ResourceName("nvidia.com/pgpu"),
		corev1.ResourceName("nvidia.com/gpu.shared"),
	}
	gpuResourcePrefixes = []string{
		"nvidia.com/mig-",
	}
	// wholeGPUResourceKeys are the resources measured in whole GPUs, so the only
	// ones that can be compared against a node's GPU capacity.
	wholeGPUResourceKeys = []corev1.ResourceName{
		corev1.ResourceName("nvidia.com/gpu"),
		corev1.ResourceName("nvidia.com/pgpu"),
	}
)

// containerNeedsComputeDomainChannel reports whether c should be given an IMEX
// channel claim, given that one node holds minGPUs whole GPUs.
func containerNeedsComputeDomainChannel(c corev1.Container, minGPUs int64) bool {
	if !containerRequestsStaticGPU(c) {
		return false
	}
	if minGPUs <= 1 {
		return true
	}
	return containerWholeGPUCount(c) >= minGPUs
}

// containerWholeGPUCount returns the largest number of whole GPUs c asks for.
// Fractional and MIG resources are deliberately not counted: a container holding
// a slice of one GPU can never own a whole node, and so can never take part in
// multi-node NVLink.
func containerWholeGPUCount(c corev1.Container) int64 {
	var count int64
	for _, rl := range []corev1.ResourceList{c.Resources.Limits, c.Resources.Requests} {
		for _, rk := range wholeGPUResourceKeys {
			if q, ok := rl[rk]; ok && q.Value() > count {
				count = q.Value()
			}
		}
	}
	return count
}

func containerRequestsStaticGPU(c corev1.Container) bool {
	var rls []corev1.ResourceList
	if c.Resources.Limits != nil {
		rls = append(rls, c.Resources.Limits)
	}
	if c.Resources.Requests != nil {
		rls = append(rls, c.Resources.Requests)
	}
	for _, rl := range rls {
		for _, rk := range gpuResourceKeys {
			if q, ok := rl[rk]; ok && !q.IsZero() {
				return true
			}
		}
		for _, prefix := range gpuResourcePrefixes {
			for rk, q := range rl {
				if strings.HasPrefix(rk.String(), prefix) && !q.IsZero() {
					return true
				}
			}
		}
	}
	return false
}

func newPartitionKey(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("x%sx", hex.EncodeToString(sum[:])[:18])
}
