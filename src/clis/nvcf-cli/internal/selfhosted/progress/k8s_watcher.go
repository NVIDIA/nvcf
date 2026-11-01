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

package progress

import (
	"context"
	"errors"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

const (
	debounceInterval = 500 * time.Millisecond

	resourceNamespaces   = "namespaces"
	resourceCRDs         = "crds"
	resourceDeployments  = "deployments"
	resourceStatefulSets = "statefulsets"
	resourceJobs         = "jobs"
)

// WatchResources watches namespaces + CRDs + Deployments + StatefulSets + Jobs
// in the given namespaces and emits a PhaseProgress event per resource type
// whenever counts change (debounced to ~500ms per resource type to avoid event
// storms during rapid initial sync).
//
// phaseNum is baked into every emitted PhaseProgress so the orchestrator
// doesn't need to wrap the events. Caller passes 4 for "Apply control plane"
// or 7 for "Apply compute plane".
//
// phaseName is baked into every emitted PhaseProgress alongside phaseNum
// so renderers (plain text, JSONL) print well-formed phase tags. Pass
// "apply-cp" for phase 4 or "apply-compute-plane" for phase 7 — match the
// orchestrator's PhaseStarted{Name} for that phase.
//
// The function blocks until ctx is cancelled. Returns ctx.Err() when ctx
// is done; nil otherwise (currently only ctx-cancellation triggers exit).
//
// CRDs are cluster-scoped and are not filtered by the namespaces list.
// All other resource types are filtered to those whose `.Namespace` (or, for
// Namespace itself, `.Name`) is present in the namespaces set.
func WatchResources(
	ctx context.Context,
	kubeClient kubernetes.Interface,
	apiExtClient apiextclientset.Interface,
	namespaces []string,
	phaseNum int,
	phaseName string,
	sink EventSink,
) error {
	if sink == nil {
		return errors.New("WatchResources: sink is required")
	}
	if len(namespaces) == 0 {
		return errors.New("WatchResources: namespaces is required")
	}

	nsSet := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		nsSet[ns] = true
	}

	factory := informers.NewSharedInformerFactory(kubeClient, 0)
	crdFactory := apiextinformers.NewSharedInformerFactory(apiExtClient, 0)

	nsInformer := factory.Core().V1().Namespaces().Informer()
	deployInformer := factory.Apps().V1().Deployments().Informer()
	ssInformer := factory.Apps().V1().StatefulSets().Informer()
	jobInformer := factory.Batch().V1().Jobs().Informer()
	crdInformer := crdFactory.Apiextensions().V1().CustomResourceDefinitions().Informer()

	nsDebouncer := newDebouncer(debounceInterval, func() {
		emitNamespaceProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	})
	crdDebouncer := newDebouncer(debounceInterval, func() {
		emitCRDProgress(ctx, sink, phaseNum, phaseName, crdFactory)
	})
	depDebouncer := newDebouncer(debounceInterval, func() {
		emitDeploymentProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	})
	ssDebouncer := newDebouncer(debounceInterval, func() {
		emitStatefulSetProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	})
	jobDebouncer := newDebouncer(debounceInterval, func() {
		emitJobProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	})

	addHandlers := func(inf cache.SharedIndexInformer, d *debouncer) {
		_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(any) { d.trigger() },
			UpdateFunc: func(any, any) { d.trigger() },
			DeleteFunc: func(any) { d.trigger() },
		})
	}

	addHandlers(nsInformer, nsDebouncer)
	addHandlers(deployInformer, depDebouncer)
	addHandlers(ssInformer, ssDebouncer)
	addHandlers(jobInformer, jobDebouncer)
	addHandlers(crdInformer, crdDebouncer)

	factory.Start(ctx.Done())
	crdFactory.Start(ctx.Done())

	factory.WaitForCacheSync(ctx.Done())
	crdFactory.WaitForCacheSync(ctx.Done())

	// Emit initial state once each (no debounce on first emit so callers
	// can synchronize on "all resource types have been emitted at least
	// once" rather than racing the informer + debounce timing).
	emitNamespaceProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	emitCRDProgress(ctx, sink, phaseNum, phaseName, crdFactory)
	emitDeploymentProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	emitStatefulSetProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)
	emitJobProgress(ctx, sink, phaseNum, phaseName, factory, nsSet)

	<-ctx.Done()

	nsDebouncer.stop()
	crdDebouncer.stop()
	depDebouncer.stop()
	ssDebouncer.stop()
	jobDebouncer.stop()

	factory.Shutdown()
	crdFactory.Shutdown()

	return ctx.Err()
}

func emitNamespaceProgress(ctx context.Context, sink EventSink, phaseNum int, phaseName string, factory informers.SharedInformerFactory, nsSet map[string]bool) {
	list, err := factory.Core().V1().Namespaces().Lister().List(labels.Everything())
	if err != nil {
		return
	}
	total, done := 0, 0
	for _, ns := range list {
		if !nsSet[ns.Name] {
			continue
		}
		total++
		if ns.Status.Phase == corev1.NamespaceActive {
			done++
		}
	}
	_ = sink.Emit(ctx, PhaseProgress{
		Num:      phaseNum,
		Name:     phaseName,
		Resource: resourceNamespaces,
		Done:     done,
		Total:    total,
	})
}

func emitCRDProgress(ctx context.Context, sink EventSink, phaseNum int, phaseName string, factory apiextinformers.SharedInformerFactory) {
	list, err := factory.Apiextensions().V1().CustomResourceDefinitions().Lister().List(labels.Everything())
	if err != nil {
		return
	}
	total, done := 0, 0
	for _, crd := range list {
		// CRDs are cluster-scoped; no nsSet filter.
		total++
		if crdEstablished(crd) {
			done++
		}
	}
	_ = sink.Emit(ctx, PhaseProgress{
		Num:      phaseNum,
		Name:     phaseName,
		Resource: resourceCRDs,
		Done:     done,
		Total:    total,
	})
}

func emitDeploymentProgress(ctx context.Context, sink EventSink, phaseNum int, phaseName string, factory informers.SharedInformerFactory, nsSet map[string]bool) {
	list, err := factory.Apps().V1().Deployments().Lister().List(labels.Everything())
	if err != nil {
		return
	}
	total, done := 0, 0
	for _, d := range list {
		if !nsSet[d.Namespace] {
			continue
		}
		total++
		// "Done" = AvailableReplicas == Replicas && Replicas > 0. The
		// Replicas > 0 guard avoids reporting a zero-replica deployment
		// (mid-creation, before status is populated) as done.
		if d.Status.Replicas > 0 && d.Status.AvailableReplicas == d.Status.Replicas {
			done++
		}
	}
	_ = sink.Emit(ctx, PhaseProgress{
		Num:      phaseNum,
		Name:     phaseName,
		Resource: resourceDeployments,
		Done:     done,
		Total:    total,
	})
}

func emitStatefulSetProgress(ctx context.Context, sink EventSink, phaseNum int, phaseName string, factory informers.SharedInformerFactory, nsSet map[string]bool) {
	list, err := factory.Apps().V1().StatefulSets().Lister().List(labels.Everything())
	if err != nil {
		return
	}
	total, done := 0, 0
	for _, s := range list {
		if !nsSet[s.Namespace] {
			continue
		}
		total++
		if s.Status.Replicas > 0 && s.Status.ReadyReplicas == s.Status.Replicas {
			done++
		}
	}
	_ = sink.Emit(ctx, PhaseProgress{
		Num:      phaseNum,
		Name:     phaseName,
		Resource: resourceStatefulSets,
		Done:     done,
		Total:    total,
	})
}

func emitJobProgress(ctx context.Context, sink EventSink, phaseNum int, phaseName string, factory informers.SharedInformerFactory, nsSet map[string]bool) {
	list, err := factory.Batch().V1().Jobs().Lister().List(labels.Everything())
	if err != nil {
		return
	}
	total, done := 0, 0
	for _, j := range list {
		if !nsSet[j.Namespace] {
			continue
		}
		total++
		// Succeeded >= 1 covers parallel jobs where Spec.Completions may
		// be > 1; we treat the job as done as soon as any pod has
		// completed successfully (matches the spec).
		if j.Status.Succeeded >= 1 {
			done++
		}
	}
	_ = sink.Emit(ctx, PhaseProgress{
		Num:      phaseNum,
		Name:     phaseName,
		Resource: resourceJobs,
		Done:     done,
		Total:    total,
	})
}

func crdEstablished(crd *apiextv1.CustomResourceDefinition) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextv1.Established && c.Status == apiextv1.ConditionTrue {
			return true
		}
	}
	return false
}

// debouncer collapses bursts of trigger() calls into a single fn() invocation
// after `delay` of quiescence. Used per-resource-type to flatten the informer
// event storm during initial cache sync.
type debouncer struct {
	mu    sync.Mutex
	timer *time.Timer
	delay time.Duration
	fn    func()
}

func newDebouncer(delay time.Duration, fn func()) *debouncer {
	return &debouncer{delay: delay, fn: fn}
}

func (d *debouncer) trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(d.delay, d.fn)
}

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
}
