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
		idxStr, ok := podTemplateAnnotation(sourceObj, RequiredNVLinkDomainIndexAnnotation)
		if !ok {
			prefNVDObjs = append(prefNVDObjs, sourceObj)
			continue
		}
		idx := reqNVDIndexMap[idxStr]
		objsByReqNVDIndex[idx] = append(objsByReqNVDIndex[idx], sourceObj)
	}

	SetPreferredNVLinkDomainSchedulingParameters(keyToHash, prefNVDObjs...)

	// Objects with no required-domain-index annotation never join a ComputeDomain: they were
	// never depending on ComputeDomain-backed placement guarantees, so no claim is attached for
	// them. Each distinct required index gets its own ComputeDomain, since a ComputeDomain
	// represents one IMEX domain and objects in different index groups are meant to join
	// different, independent NVLink domains.
	indices := make([]int, 0, len(objsByReqNVDIndex))
	for idx := range objsByReqNVDIndex {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	cds := make([]client.Object, 0, len(indices))
	for _, idx := range indices {
		objs := objsByReqNVDIndex[idx]
		cd := computeDomainForIndex(idx)
		SetComputeDomainToGPUPodResourceClaims(cd, objs...)
		SetRequiredNVLinkDomainSchedulingParameters(keyToHash, fmt.Sprint(idx), objs...)
		cds = append(cds, cd)
	}

	return sourceObjs, cds, nil
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
		idx, ok := podTemplateAnnotation(sourceObj, RequiredNVLinkDomainIndexAnnotation)
		if !ok {
			continue
		}
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
	computeDomainNamePrefix        = "nvcf-cd-index"
	computeDomainChannelNamePrefix = "nvcf-cd-channel"
)

// ComputeDomainRef identifies a ComputeDomain and the name of the ResourceClaimTemplate that
// backs its channel, without requiring callers to hold the full ComputeDomain object. It is the
// shape persisted into the MiniserviceMetadata ConfigMap so the admission webhook can attach
// claims to a pod without recomputing or renaming the ComputeDomain itself.
type ComputeDomainRef struct {
	ComputeDomainName string `json:"computeDomainName"`
	ChannelName       string `json:"channelName"`
}

// ComputeDomainFromRef builds the ComputeDomain object identified by ref. Used by callers (the
// admission webhook) that only have a ComputeDomainRef, not the originating object set.
func ComputeDomainFromRef(ref ComputeDomainRef) *nvresourcev1beta1.ComputeDomain {
	return &nvresourcev1beta1.ComputeDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: ref.ComputeDomainName,
		},
		Spec: nvresourcev1beta1.ComputeDomainSpec{
			Channel: &nvresourcev1beta1.ComputeDomainChannelSpec{
				ResourceClaimTemplate: nvresourcev1beta1.ComputeDomainResourceClaimTemplate{
					Name: ref.ChannelName,
				},
			},
		},
	}
}

// computeDomainForIndex builds the ComputeDomain for a single normalized NVLink domain index.
// Naming is index-suffixed so that objects declaring different required-nvlink-domain-index
// values are backed by distinct ComputeDomains, matching the one-IMEX-domain-per-ComputeDomain
// invariant: a ComputeDomain represents one IMEX domain, so objects meant to join different,
// independent NVLink domains must not share one.
func computeDomainForIndex(idx int) *nvresourcev1beta1.ComputeDomain {
	return ComputeDomainFromRef(ComputeDomainRef{
		ComputeDomainName: fmt.Sprintf("%s-%d", computeDomainNamePrefix, idx),
		ChannelName:       fmt.Sprintf("%s-%d", computeDomainChannelNamePrefix, idx),
	})
}

// podTemplateAnnotation returns the value of annotation key on obj's pod template (or on obj
// itself when obj is a bare Pod, since a Pod is its own template), and whether it was present.
// This is the same location Kubernetes copies onto the Pods a Deployment/StatefulSet/Job/CronJob
// creates, so it is the only place an annotation set here is guaranteed to reach the admission
// webhook, which only ever sees realized Pods. A top-level annotation on the controller object
// itself (e.g. a Deployment's own ObjectMeta) is never copied down and would never reach a Pod.
func podTemplateAnnotation(obj client.Object, key string) (string, bool) {
	var val string
	var ok bool
	itrf := func(pts *corev1.PodTemplateSpec) {
		if v, found := pts.Annotations[key]; found {
			val, ok = v, true
		}
	}
	iterPodSpecs(itrf, obj)
	return val, ok
}

// anyPodHasRequiredNVLinkDomainIndex reports whether any pod template among objs carries the
// RequiredNVLinkDomainIndexAnnotation.
func anyPodHasRequiredNVLinkDomainIndex(objs ...client.Object) bool {
	for _, obj := range objs {
		if _, ok := podTemplateAnnotation(obj, RequiredNVLinkDomainIndexAnnotation); ok {
			return true
		}
	}
	return false
}

// ComputeDomainsForWorkload scans objs' pod templates for the required-nvlink-domain-index
// annotation and returns one ComputeDomain per distinct raw index value present, plus a mapping
// from each raw annotation value to a ComputeDomainRef identifying the ComputeDomain that backs
// it. If no pod template carries the annotation, it returns (nil, nil, nil): no ComputeDomain is
// needed if nothing declares an NVLink domain requirement.
func ComputeDomainsForWorkload(objs ...client.Object) ([]*nvresourcev1beta1.ComputeDomain, map[string]ComputeDomainRef, error) {
	if !anyPodHasRequiredNVLinkDomainIndex(objs...) {
		return nil, nil, nil
	}

	idxMap, err := sanitizeIndices(objs)
	if err != nil {
		return nil, nil, err
	}

	cdByIdx := make(map[int]*nvresourcev1beta1.ComputeDomain, len(idxMap))
	refByRaw := make(map[string]ComputeDomainRef, len(idxMap))
	for raw, idx := range idxMap {
		cd, ok := cdByIdx[idx]
		if !ok {
			cd = computeDomainForIndex(idx)
			cdByIdx[idx] = cd
		}
		refByRaw[raw] = ComputeDomainRef{
			ComputeDomainName: cd.Name,
			ChannelName:       cd.Spec.Channel.ResourceClaimTemplate.Name,
		}
	}

	cds := make([]*nvresourcev1beta1.ComputeDomain, 0, len(cdByIdx))
	for _, cd := range cdByIdx {
		cds = append(cds, cd)
	}
	sort.Slice(cds, func(i, j int) bool { return cds[i].Name < cds[j].Name })

	return cds, refByRaw, nil
}

func SetComputeDomainToGPUPodResourceClaims(
	cd *nvresourcev1beta1.ComputeDomain,
	objs ...client.Object,
) {
	mf := func(pts *corev1.PodTemplateSpec) {
		ps := &pts.Spec
		anyUpdated := false
		for ci, c := range append(ps.Containers, ps.InitContainers...) {
			if containerRequestsStaticGPU(c) {
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
			// TODO: third-party operator types (e.g. DynamoGraphDeployment) are invisible to
			// the NVLink domain-index scan below since their pod templates aren't one of the
			// well-known kinds above. When Karta's generic Pod metadata accessor is vendored
			// (https://github.com/run-ai/karta/blob/main/pkg/resource/accessor.go#L88), use it
			// here to generically extract pod-template annotations from arbitrary object kinds
			// so those types can also carry the required-nvlink-domain-index annotation.
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
)

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
