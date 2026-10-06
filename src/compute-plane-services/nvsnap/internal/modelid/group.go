// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package modelid

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Group inheritance. A LeaderWorkerSet worker runs `vllm serve --headless`
// or `ray start --block` and names no model; the leader template does.
// The worker still needs the same bytes on its node before the group can
// form, so it inherits the leader's identity. StatefulSet and Dynamo
// members carry their own model argument and never reach this path.

// LWS pod labels and the LeaderWorkerSet resource.
const (
	lwsNameLabel        = "leaderworkerset.sigs.k8s.io/name"
	lwsWorkerIndexLabel = "leaderworkerset.sigs.k8s.io/worker-index"
)

var lwsGVR = schema.GroupVersionResource{Group: "leaderworkerset.x-k8s.io", Version: "v1", Resource: "leaderworkersets"}

// GroupResolver finds the identity of the group a pod belongs to.
type GroupResolver interface {
	// ResolveGroup returns the group's result and true when pod is a
	// member of a group whose leader template names a model.
	ResolveGroup(ctx context.Context, pod *corev1.Pod) (Result, bool, error)
}

// LWSResolver reads the LeaderWorkerSet's leader template.
type LWSResolver struct {
	Dyn dynamic.Interface
}

// ResolveGroup implements GroupResolver for LWS workers.
func (r *LWSResolver) ResolveGroup(ctx context.Context, pod *corev1.Pod) (Result, bool, error) {
	if r == nil || r.Dyn == nil || pod == nil {
		return Result{}, false, nil
	}
	name := pod.Labels[lwsNameLabel]
	if name == "" {
		return Result{}, false, nil
	}
	if idx := pod.Labels[lwsWorkerIndexLabel]; idx == "0" || idx == "" {
		return Result{}, false, nil // the leader resolves on its own
	}
	lws, err := r.Dyn.Resource(lwsGVR).Namespace(pod.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Result{}, false, fmt.Errorf("get LeaderWorkerSet %s/%s: %w", pod.Namespace, name, err)
	}
	tmpl, found, err := unstructured.NestedMap(lws.Object, "spec", "leaderWorkerTemplate", "leaderTemplate")
	if err != nil || !found {
		return Result{}, false, nil
	}
	var leader corev1.PodTemplateSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(tmpl, &leader); err != nil {
		return Result{}, false, fmt.Errorf("decode leader template: %w", err)
	}
	leaderPod := &corev1.Pod{ObjectMeta: leader.ObjectMeta, Spec: leader.Spec}
	res, ok := Resolve(leaderPod, 0)
	if !ok {
		return Result{}, false, nil
	}
	// The worker's own landing volume is what it mounts; the leader's
	// tells us only the identity and the path convention.
	res.Landing = landingFor(pod, &pod.Spec.Containers[0], res.Landing.Path, res.Landing.Downloader, "")
	res.Source = "lws leader template: " + res.Source
	return res, true, nil
}

// ResolveWithGroup is Resolve followed by group inheritance for pods that
// name no model themselves.
func ResolveWithGroup(ctx context.Context, pod *corev1.Pod, mainContainer int, groups GroupResolver) (Result, bool, error) {
	if res, ok := Resolve(pod, mainContainer); ok {
		return res, true, nil
	}
	if groups == nil {
		return Result{}, false, nil
	}
	return groups.ResolveGroup(ctx, pod)
}
