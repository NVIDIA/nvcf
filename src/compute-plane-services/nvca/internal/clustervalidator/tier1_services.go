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

package clustervalidator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// helmReleaseAnnotation is the annotation Helm stamps on every object it
// applies with the name of its release.
const helmReleaseAnnotation = "meta.helm.sh/release-name"

// haInstanceLabel is the pod label the stack's charts set to the helmfile
// release name.
const haInstanceLabel = "app.kubernetes.io/instance"

// tier1ServiceAdvice is the advice for a Service that selects none of its
// release's pods.
const tier1ServiceAdvice = "A Service that selects none of its release's pods has no endpoint, so the routes to " +
	"it fail although its workload is Ready. An upgrade that changed the pod labels leaves this when it patched " +
	"the Service but could not change the workload's immutable selector. Check the release " +
	"(helm -n <namespace> history <release>), then roll it back, or recreate the workload so its pods carry the " +
	"labels the Service selects."

// releaseOf is the Helm release that installed obj: the one Helm stamped on
// it, or else its app.kubernetes.io/instance label.
func releaseOf(obj metav1.Object) string {
	if r := obj.GetAnnotations()[helmReleaseAnnotation]; r != "" {
		return r
	}
	return obj.GetLabels()[haInstanceLabel]
}

// judgeServices finds, after install, each Service of a stack release in ns
// whose selector matches no pod that serves or may yet serve, while a
// Deployment or StatefulSet of its release has Ready pods labelled otherwise
// (selectorGap). A pod that has finished, such as a release's hook pod, or is
// terminating serves nothing, so it does not count. A Service that selects a
// pod that is down is left to the readiness findings, as is one whose release
// has no Ready pod, and the headless Service a StatefulSet governs is not
// judged. It lists the Services of ns once, its pods once when a Service is
// judged, and its StatefulSets once when one selects no such pod.
func (s *tier1Scan) judgeServices(
	ctx context.Context, client kubernetes.Interface, ns string, deploys []appsv1.Deployment,
) {
	services, err := observe(ctx, func(c context.Context) (*corev1.ServiceList, error) {
		return client.CoreV1().Services(ns).List(c, metav1.ListOptions{})
	})
	if err != nil {
		s.warn(readFailure("Services in "+ns, err))
		s.servicesUnread = true
		return
	}
	var judged []*corev1.Service
	for i := range services.Items {
		svc := &services.Items[i]
		if svc.Spec.Type == corev1.ServiceTypeExternalName || len(svc.Spec.Selector) == 0 || releaseOf(svc) == "" ||
			(s.shared[ns] && !stackOwned(ns, svc)) {
			continue
		}
		judged = append(judged, svc)
	}
	if len(judged) == 0 {
		return
	}
	pods, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
		return client.CoreV1().Pods(ns).List(c, metav1.ListOptions{})
	})
	if err != nil {
		s.warn(readFailure("pods in "+ns, err))
		s.servicesUnread = true
		return
	}
	var sets *appsv1.StatefulSetList
	for _, svc := range judged {
		sel := labels.SelectorFromSet(svc.Spec.Selector)
		if slices.ContainsFunc(pods.Items, func(p corev1.Pod) bool {
			return mayServe(&p) && sel.Matches(labels.Set(p.Labels))
		}) {
			continue
		}
		if sets == nil {
			if sets, err = observe(ctx, func(c context.Context) (*appsv1.StatefulSetList, error) {
				return client.AppsV1().StatefulSets(ns).List(c, metav1.ListOptions{})
			}); err != nil {
				s.warn(readFailure("StatefulSets in "+ns, err))
				s.servicesUnread = true
				return
			}
		}
		if gap := selectorGap(svc, deploys, sets.Items, pods.Items); gap != "" {
			s.serviceGaps = append(s.serviceGaps, gap)
		}
	}
}

// podWorkload is a Deployment or StatefulSet as selectorGap judges it.
type podWorkload struct {
	kind     string
	obj      metav1.Object
	selector *metav1.LabelSelector
	labels   map[string]string
}

// mayServe reports whether p serves, or may yet serve, a Service's traffic:
// it is neither terminating nor finished.
func mayServe(p *corev1.Pod) bool {
	return p.DeletionTimestamp == nil && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed
}

// selectorGap says how svc, whose selector matches no pod in its namespace
// that may serve, selects none of the Ready pods of a workload of its release
// (relabelled), or is "" when nothing shows that.
func selectorGap(svc *corev1.Service, deploys []appsv1.Deployment, sets []appsv1.StatefulSet,
	pods []corev1.Pod) string {
	var workloads []podWorkload
	for i := range deploys {
		d := &deploys[i]
		workloads = append(workloads, podWorkload{"Deployment", d, d.Spec.Selector, d.Spec.Template.Labels})
	}
	for i := range sets {
		st := &sets[i]
		if svc.Spec.ClusterIP == corev1.ClusterIPNone && st.Spec.ServiceName == svc.Name {
			return ""
		}
		workloads = append(workloads, podWorkload{"StatefulSet", st, st.Spec.Selector, st.Spec.Template.Labels})
	}
	sel := labels.SelectorFromSet(svc.Spec.Selector)
	if slices.ContainsFunc(workloads, func(w podWorkload) bool { return sel.Matches(labels.Set(w.labels)) }) {
		return ""
	}
	release := releaseOf(svc)
	var own []podWorkload
	var ready [][]*corev1.Pod
	var releaseReady []*corev1.Pod
	for _, w := range workloads {
		if releaseOf(w.obj) == release {
			r := readyPodsSelected(w.selector, pods)
			own, ready, releaseReady = append(own, w), append(ready, r), append(releaseReady, r...)
		}
	}
	// The workload named is the one the selector names by the most labels it
	// carries with their values; one it names by none is not the Service's.
	best, most := -1, 0
	for i, w := range own {
		if len(ready[i]) == 0 || !relabelled(svc.Spec.Selector, w.labels, ready[i], releaseReady) {
			continue
		}
		if n := agreeing(svc.Spec.Selector, w.labels); n > most {
			best, most = i, n
		}
	}
	if best < 0 {
		return ""
	}
	w := own[best]
	carried := labels.Set{}
	var lacked []string
	for k := range svc.Spec.Selector {
		if v, ok := w.labels[k]; ok {
			carried[k] = v
		} else if !carriedBy(ready[best], k) {
			lacked = append(lacked, k)
		}
	}
	labelled := "labelled " + carried.String()
	if len(lacked) > 0 {
		slices.Sort(lacked)
		labelled += ", with no " + strings.Join(lacked, " or ") + " label"
	}
	return fmt.Sprintf("Service %s/%s selects %s, which matches no pod, while the %d Ready pod(s) of %s %s/%s of "+
		"release %s are %s", svc.Namespace, svc.Name, labels.Set(svc.Spec.Selector), len(ready[best]), w.kind,
		w.obj.GetNamespace(), w.obj.GetName(), release, labelled)
}

// agreeing counts the keys of selector that tpl carries with its value.
func agreeing(selector, tpl map[string]string) int {
	n := 0
	for k, v := range selector {
		if have, ok := tpl[k]; ok && have == v {
			n++
		}
	}
	return n
}

// relabelled reports whether selector misses a workload whose template labels
// are tpl and whose Ready pods are own: tpl carries one of its keys with
// another value or, as an upgrade that adds a key to the selector leaves it,
// not at all. A key own carries but tpl does not is a label the pods gain at
// run time, such as OpenBao's active label, and shows no mismatch. One only
// other Ready pods of the release carry, in releaseReady, says the Service is
// for those pods.
func relabelled(selector, tpl map[string]string, own, releaseReady []*corev1.Pod) bool {
	differs := false
	for k, v := range selector {
		have, ok := tpl[k]
		switch {
		case ok:
			differs = differs || have != v
		case carriedBy(own, k):
		case carriedBy(releaseReady, k):
			return false
		default:
			differs = true
		}
	}
	return differs
}

// carriedBy reports whether one of pods carries label key.
func carriedBy(pods []*corev1.Pod, key string) bool {
	return slices.ContainsFunc(pods, func(p *corev1.Pod) bool { _, ok := p.Labels[key]; return ok })
}

// readyPodsSelected returns the Ready members among pods that sel selects.
func readyPodsSelected(sel *metav1.LabelSelector, pods []corev1.Pod) []*corev1.Pod {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil || selector.Empty() {
		return nil
	}
	var out []*corev1.Pod
	for i := range pods {
		if selector.Matches(labels.Set(pods[i].Labels)) && readyMember(&pods[i]) {
			out = append(out, &pods[i])
		}
	}
	return out
}
