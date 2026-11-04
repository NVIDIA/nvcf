// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package election

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Node labels the GPU Operator's feature discovery sets on GPU nodes. The
// compute capability they spell is the boundary of a compile cache:
// Triton, torch.compile and TensorRT artifacts built for one do not run
// on another.
const (
	ComputeMajorLabel = "nvidia.com/gpu.compute.major"
	ComputeMinorLabel = "nvidia.com/gpu.compute.minor"
)

// ComputeCapability spells the compute capability labels carry as
// "<major>.<minor>"; empty when the major is absent.
func ComputeCapability(labels map[string]string) string {
	major := labels[ComputeMajorLabel]
	if major == "" {
		return ""
	}
	if minor := labels[ComputeMinorLabel]; minor != "" {
		return major + "." + minor
	}
	return major
}

// ArchScope establishes at admission the GPU architecture a pod will run
// on. ok is false when it cannot be established, and the pod then stays
// out of the election: sharing a cache across architectures would hand
// one of them kernels it cannot run.
type ArchScope interface {
	ComputeCapability(ctx context.Context, pod *corev1.Pod) (cc string, ok bool, err error)
}

// NodeArchScope establishes it from the nodes: the compute capability is
// the one every GPU node the pod may be scheduled on shares. A pod that
// could land on GPU nodes of two architectures, on a GPU node without the
// labels, or on none, has no established architecture.
//
// The nodes counted are those the pod's node selector and required node
// affinity admit. Taints and cordons are not considered: they only
// narrow where a pod goes, so counting those nodes too can only find more
// architectures, never fewer.
type NodeArchScope struct {
	KubeClient kubernetes.Interface
}

// ComputeCapability implements ArchScope.
func (s *NodeArchScope) ComputeCapability(ctx context.Context, pod *corev1.Pod) (cc string, ok bool, err error) {
	if s == nil || s.KubeClient == nil {
		return "", false, nil
	}
	nodes, err := s.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", false, fmt.Errorf("list nodes: %w", err)
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if !hasGPU(n) || !Admits(pod, n) {
			continue
		}
		c := ComputeCapability(n.Labels)
		if c == "" || (cc != "" && c != cc) {
			return "", false, nil
		}
		cc = c
	}
	return cc, cc != "", nil
}

func hasGPU(n *corev1.Node) bool {
	q, ok := n.Status.Allocatable["nvidia.com/gpu"]
	return ok && !q.IsZero()
}

// Admits reports whether pod's node selector and required node affinity
// admit node, as the scheduler reads them: every selector label equal,
// and at least one affinity term whose every expression and field
// requirement holds. An operator it does not know admits the node, which
// can only widen the nodes counted.
func Admits(pod *corev1.Pod, node *corev1.Node) bool {
	for k, v := range pod.Spec.NodeSelector {
		if node.Labels[k] != v {
			return false
		}
	}
	a := pod.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return true
	}
	for _, term := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		if termAdmits(term, node) {
			return true
		}
	}
	return false
}

// termAdmits: a term with no requirements matches no node.
func termAdmits(term corev1.NodeSelectorTerm, node *corev1.Node) bool {
	if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
		return false
	}
	for _, r := range term.MatchExpressions {
		v, has := node.Labels[r.Key]
		if !requirementHolds(r, v, has) {
			return false
		}
	}
	for _, r := range term.MatchFields {
		if r.Key != "metadata.name" {
			continue
		}
		if !requirementHolds(r, node.Name, true) {
			return false
		}
	}
	return true
}

func requirementHolds(r corev1.NodeSelectorRequirement, v string, has bool) bool {
	in := func() bool {
		for _, want := range r.Values {
			if v == want {
				return true
			}
		}
		return false
	}
	compare := func(gt bool) bool {
		if !has || len(r.Values) != 1 {
			return false
		}
		got, err1 := strconv.ParseInt(v, 10, 64)
		want, err2 := strconv.ParseInt(r.Values[0], 10, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		if gt {
			return got > want
		}
		return got < want
	}
	switch r.Operator {
	case corev1.NodeSelectorOpIn:
		return has && in()
	case corev1.NodeSelectorOpNotIn:
		return !has || !in()
	case corev1.NodeSelectorOpExists:
		return has
	case corev1.NodeSelectorOpDoesNotExist:
		return !has
	case corev1.NodeSelectorOpGt:
		return compare(true)
	case corev1.NodeSelectorOpLt:
		return compare(false)
	}
	return true
}

// PinnedComputeCapability is the compute capability pod's node selector
// pins it to (ArchPin), empty when it pins none.
func PinnedComputeCapability(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	return ComputeCapability(pod.Spec.NodeSelector)
}

// ArchPin is the node selector that keeps a pod on cc: the labels a node
// of that compute capability carries.
func ArchPin(cc string) map[string]string {
	major, minor := cc, ""
	for i := 0; i < len(cc); i++ {
		if cc[i] == '.' {
			major, minor = cc[:i], cc[i+1:]
			break
		}
	}
	pin := map[string]string{ComputeMajorLabel: major}
	if minor != "" {
		pin[ComputeMinorLabel] = minor
	}
	return pin
}
