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
	"strings"

	nvresourcev1beta1 "github.com/NVIDIA/k8s-dra-driver-gpu/api/nvidia.com/resource/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

const (
	// defaultComputeDomainName / defaultComputeDomainChannelName are the names NVCA has
	// always used for the single per-MiniService ComputeDomain. They are retained verbatim
	// as the domain for pods that declare no NVLink domain index, and are always created
	// while NVLinkOptimized is active, so that rolling the agent back to a build that
	// unconditionally claims this name still finds it. Renaming or omitting them turns a
	// rollback into unschedulable pods.
	defaultComputeDomainName        = "nvcf-cd-index-0"
	defaultComputeDomainChannelName = "nvcf-cd-channel-0"

	computeDomainNamePrefix        = "nvcf-cd"
	computeDomainChannelNamePrefix = "nvcf-cd-channel"
)

// NewSingleChannelComputeDomain returns the default ComputeDomain, used by pods that declare
// no required-nvlink-domain-index.
func NewSingleChannelComputeDomain() *nvresourcev1beta1.ComputeDomain {
	return newComputeDomain(defaultComputeDomainName, defaultComputeDomainChannelName)
}

func newComputeDomain(name, channelName string) *nvresourcev1beta1.ComputeDomain {
	return &nvresourcev1beta1.ComputeDomain{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: nvresourcev1beta1.ComputeDomainSpec{
			Channel: &nvresourcev1beta1.ComputeDomainChannelSpec{
				ResourceClaimTemplate: nvresourcev1beta1.ComputeDomainResourceClaimTemplate{
					Name: channelName,
				},
			},
		},
	}
}

// ComputeDomainForIndex returns the ComputeDomain backing a raw required-nvlink-domain-index
// annotation value.
//
// The name is derived from the raw value itself, not from its rank among the values present in
// a render. A rank changes when another value is added, which would rename an existing domain
// while running pods still claim the old name. Deriving from the value means the reconciler and
// the admission webhook independently compute the same name from the same input, with no shared
// state, no ordering assumption, and nothing to keep in sync across processes.
//
// The value is hashed because it is tenant-authored and need not be a legal object name.
func ComputeDomainForIndex(rawIndex string) *nvresourcev1beta1.ComputeDomain {
	if rawIndex == "" {
		return NewSingleChannelComputeDomain()
	}
	h := newPartitionKey([]byte(rawIndex))
	return newComputeDomain(
		fmt.Sprintf("%s-%s", computeDomainNamePrefix, h),
		fmt.Sprintf("%s-%s", computeDomainChannelNamePrefix, h),
	)
}

// podTemplateAnnotation returns the value of annotation key for obj, and whether it was present.
//
// For built-in workload kinds it reads the pod template, the only location Kubernetes copies
// down onto created Pods. Objects of a kind NVCA does not have a Go type for -- Grove
// PodCliqueSet, DynamoGraphDeployment and anything else in allowedExtraKubernetesTypes -- decode
// to *unstructured.Unstructured and carry their pod templates at operator-defined paths, so they
// are searched for any nested annotations map instead. Skipping them would leave the operator's
// realized Pods with the annotation the webhook acts on but no ComputeDomain to claim.
func podTemplateAnnotation(obj client.Object, key string) (string, bool) {
	if u, isUnstructured := obj.(*unstructured.Unstructured); isUnstructured {
		return nestedAnnotation(u.Object, key)
	}
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

// nestedAnnotation searches obj for an "annotations" map containing key, below the object's own
// top-level metadata. A custom resource's own metadata.annotations is not a pod template and is
// not copied onto its Pods, so it is excluded; every deeper annotations map belongs to some
// template the operator stamps out.
func nestedAnnotation(obj map[string]any, key string) (string, bool) {
	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		return "", false
	}
	return searchAnnotations(spec, key)
}

func searchAnnotations(node any, key string) (string, bool) {
	switch n := node.(type) {
	case map[string]any:
		if annos, ok := n["annotations"].(map[string]any); ok {
			if v, found := annos[key]; found {
				if s, isStr := v.(string); isStr {
					return s, true
				}
			}
		}
		// Iterate deterministically: a template may appear under any key, and two
		// different values must not resolve differently run to run.
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v, found := searchAnnotations(n[k], key); found {
				return v, true
			}
		}
	case []any:
		for _, item := range n {
			if v, found := searchAnnotations(item, key); found {
				return v, true
			}
		}
	}
	return "", false
}

// ComputeDomainsForWorkload returns every ComputeDomain a MiniService needs: the default domain,
// plus one per distinct required-nvlink-domain-index value declared by its workload objects.
//
// The default domain is always included. It backs pods that declare no index, and it is the name
// an older agent claims unconditionally, so creating it keeps a rollback survivable.
func ComputeDomainsForWorkload(objs ...client.Object) []*nvresourcev1beta1.ComputeDomain {
	cds := []*nvresourcev1beta1.ComputeDomain{NewSingleChannelComputeDomain()}
	seen := sets.New[string](defaultComputeDomainName)
	raws := sets.New[string]()
	for _, obj := range objs {
		if raw, ok := podTemplateAnnotation(obj, RequiredNVLinkDomainIndexAnnotation); ok && raw != "" {
			raws.Insert(raw)
		}
	}
	for _, raw := range sets.List(raws) {
		cd := ComputeDomainForIndex(raw)
		if seen.Has(cd.Name) {
			continue
		}
		seen.Insert(cd.Name)
		cds = append(cds, cd)
	}
	return cds
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
		requireGPUCliqueNode(ps.Affinity)
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
		requireGPUCliqueNode(ps.Affinity)
	}

	iterPodSpecs(itrf, objs...)
}

var gpuCliqueNodeSelectorRequirement = corev1.NodeSelectorRequirement{
	Key:      GPUCliqueNodeLabel,
	Operator: corev1.NodeSelectorOpExists,
}

// requireGPUCliqueNode narrows the required node affinity of a Pod so that it
// only admits nodes carrying a GPU clique label.
//
// Kubernetes ORs NodeSelectorTerms, so the requirement is ANDed into every
// existing term instead of being appended as a term of its own. An appended
// clique-only term would give the Pod an alternative way to satisfy required
// node affinity and let it bypass constraints it already carries, such as a
// hostname restriction. The merge is idempotent so that repeated admission
// does not accumulate duplicate requirements.
func requireGPUCliqueNode(a *corev1.Affinity) {
	if a.NodeAffinity == nil {
		a.NodeAffinity = &corev1.NodeAffinity{}
	}
	if a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	ns := a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if len(ns.NodeSelectorTerms) == 0 {
		ns.NodeSelectorTerms = []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{gpuCliqueNodeSelectorRequirement},
		}}
		return
	}
	for i := range ns.NodeSelectorTerms {
		term := &ns.NodeSelectorTerms[i]
		if hasGPUCliqueRequirement(term.MatchExpressions) {
			continue
		}
		term.MatchExpressions = append(term.MatchExpressions, gpuCliqueNodeSelectorRequirement)
	}
}

func hasGPUCliqueRequirement(reqs []corev1.NodeSelectorRequirement) bool {
	for _, req := range reqs {
		if req.Key == GPUCliqueNodeLabel && req.Operator == corev1.NodeSelectorOpExists {
			return true
		}
	}
	return false
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
