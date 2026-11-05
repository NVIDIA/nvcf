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

package nvca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// nodeCordonSignaler publishes the cordon state of a node on the NVCF worker pods that run on it.
//
// Kubernetes does not tell a running pod that its node was cordoned. The signaler copies the state onto each
// worker pod as the nvcatypes.NodeCordonedAnnotationKey annotation. The pod webhook mounts that annotation
// as a file through a downward API volume, so a worker can stop taking new sessions and leave once its
// current session ends. Nothing is evicted here: the signaler only reports the state.
//
// It does nothing unless the featureflag.NodeCordonSignal flag is enabled.
type nodeCordonSignaler struct {
	pods  listersv1.PodLister
	nodes listersv1.NodeLister
	k8s   kubernetes.Interface
	fff   featureflag.Fetcher
	queue workqueue.TypedRateLimitingInterface[string]
	now   func() time.Time
}

func newNodeCordonSignaler(
	pods listersv1.PodLister,
	nodes listersv1.NodeLister,
	k8s kubernetes.Interface,
	fff featureflag.Fetcher,
) *nodeCordonSignaler {
	return &nodeCordonSignaler{
		pods:  pods,
		nodes: nodes,
		k8s:   k8s,
		fff:   fff,
		queue: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
		now:   time.Now,
	}
}

// addNodeHandler queues a node when it is first seen, when it is cordoned or uncordoned, and on every
// periodic resync. Seeing a node at start repairs the state after an NVCA restart, and the resync repairs
// events that were missed.
func (s *nodeCordonSignaler) addNodeHandler(ctx context.Context, inf cache.SharedIndexInformer) error {
	log := core.GetLogger(ctx)
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			node, ok := obj.(*corev1.Node)
			if !ok {
				log.Errorf("expected *corev1.Node, got %T", obj)
				return
			}
			s.queue.Add(node.Name)
		},
		UpdateFunc: func(oldObj, newObj any) {
			oldNode, okOld := oldObj.(*corev1.Node)
			newNode, okNew := newObj.(*corev1.Node)
			if !okOld || !okNew {
				log.Errorf("expected *corev1.Node, got %T and %T", oldObj, newObj)
				return
			}
			if shouldReconcileNodeUpdate(oldNode, newNode) {
				s.queue.Add(newNode.Name)
			}
		},
	})
	return err
}

// shouldReconcileNodeUpdate reports whether a node update can change the signal: the node was cordoned or
// uncordoned, or it is a periodic resync, which the informer delivers with an unchanged resource version.
func shouldReconcileNodeUpdate(oldNode, newNode *corev1.Node) bool {
	return oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable ||
		oldNode.ResourceVersion == newNode.ResourceVersion
}

// run starts workers that process queued nodes until ctx is done.
func (s *nodeCordonSignaler) run(ctx context.Context, workers int) {
	go func() {
		<-ctx.Done()
		s.queue.ShutDown()
	}()
	for range workers {
		go func() {
			for s.processNextItem(ctx) {
			}
		}()
	}
}

// processNextItem handles one queued node. It returns false once the queue is shut down.
func (s *nodeCordonSignaler) processNextItem(ctx context.Context) bool {
	name, shutdown := s.queue.Get()
	if shutdown {
		return false
	}
	defer s.queue.Done(name)

	log := core.GetLogger(ctx).WithField("node", name)
	if !s.fff.IsFeatureFlagEnabled(featureflag.NodeCordonSignal) {
		s.queue.Forget(name)
		return true
	}

	node, err := s.nodes.Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.queue.Forget(name)
			return true
		}
		log.WithError(err).Error("failed to get the node to publish its cordon state")
		s.queue.AddRateLimited(name)
		return true
	}

	if err := s.reconcileNode(ctx, node); err != nil {
		log.WithError(err).Error("failed to publish the node cordon state on worker pods")
		s.queue.AddRateLimited(name)
		return true
	}
	s.queue.Forget(name)
	return true
}

// reconcileNode makes every live NVCF worker pod on node carry the annotation exactly while the node is
// cordoned. An annotation that is already set keeps the time at which the cordon was first seen. A failure
// on one pod does not stop the others, and the errors are returned together so the node is retried.
func (s *nodeCordonSignaler) reconcileNode(ctx context.Context, node *corev1.Node) error {
	hasRequestID, err := labels.NewRequirement(nvcatypes.ICMSRequestIDKey, selection.Exists, nil)
	if err != nil {
		return fmt.Errorf("build worker pod selector: %w", err)
	}
	pods, err := s.pods.List(labels.NewSelector().Add(*hasRequestID))
	if err != nil {
		return fmt.Errorf("list worker pods: %w", err)
	}

	var errs []error
	for _, pod := range pods {
		if pod.Spec.NodeName != node.Name || pod.DeletionTimestamp != nil ||
			pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		marked := pod.Annotations[nvcatypes.NodeCordonedAnnotationKey] != ""
		switch {
		case node.Spec.Unschedulable && !marked:
			errs = append(errs, s.setCordonAnnotation(ctx, pod, s.now().UTC().Format(time.RFC3339)))
		case !node.Spec.Unschedulable && marked:
			errs = append(errs, s.setCordonAnnotation(ctx, pod, ""))
		}
	}
	return errors.Join(errs...)
}

// setCordonAnnotation sets the annotation on pod, or removes it when value is empty. A pod that is gone
// is not an error.
func (s *nodeCordonSignaler) setCordonAnnotation(ctx context.Context, pod *corev1.Pod, value string) error {
	var annotation any
	if value != "" {
		annotation = value // a nil value removes the key in a merge patch
	}
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{nvcatypes.NodeCordonedAnnotationKey: annotation},
		},
	})
	if err != nil {
		return fmt.Errorf("build patch for pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	_, err = s.k8s.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, apitypes.MergePatchType, patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("patch pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}

	log := core.GetLogger(ctx).WithField("node", pod.Spec.NodeName).WithField("pod", pod.Namespace+"/"+pod.Name)
	if value == "" {
		log.Info("node is schedulable again, cleared the cordon signal on the worker pod")
	} else {
		log.Info("node is cordoned, signaled the worker pod")
	}
	return nil
}
