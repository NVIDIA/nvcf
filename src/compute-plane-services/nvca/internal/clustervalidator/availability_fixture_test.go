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
	"bytes"
	"context"
	"fmt"
	"maps"
	"testing"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

// haFixture is a self-managed install as an HA mode renders it, healthy: each
// workload of haWorkloads with its PDB and its Ready pods spread over three
// nodes in three zones. Tests change it before running the HA Guarantee.
type haFixture struct {
	mode    HAMode
	nodes   []*corev1.Node
	deploys map[string]*appsv1.Deployment
	sets    map[string]*appsv1.StatefulSet
	pdbs    map[string]*policyv1.PodDisruptionBudget
	pods    map[string][]*corev1.Pod
	extra   []runtime.Object
}

// haObjectName is the name the stack's charts give the workload of release.
func haObjectName(release string) string {
	if release == "api" {
		return "nvcf-api"
	}
	return release
}

// haPodLabels are the pod labels of release's workload.
func haPodLabels(w haWorkload) map[string]string {
	l := map[string]string{"app.kubernetes.io/name": "helm-" + w.release, haInstanceLabel: w.release}
	maps.Copy(l, w.podLabels)
	return l
}

// haPlacement is the hostname anti-affinity and zone spread mode renders for
// pods labelled sel.
func haPlacement(mode HAMode, sel map[string]string) (*corev1.Affinity, []corev1.TopologySpreadConstraint) {
	term := corev1.PodAffinityTerm{LabelSelector: &metav1.LabelSelector{MatchLabels: sel},
		TopologyKey: corev1.LabelHostname}
	aa := &corev1.PodAntiAffinity{}
	when := corev1.ScheduleAnyway
	if mode == HAModeEnforced {
		aa.RequiredDuringSchedulingIgnoredDuringExecution = []corev1.PodAffinityTerm{term}
		when = corev1.DoNotSchedule
	} else {
		aa.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.WeightedPodAffinityTerm{
			{Weight: 100, PodAffinityTerm: term}}
	}
	return &corev1.Affinity{PodAntiAffinity: aa}, []corev1.TopologySpreadConstraint{{
		MaxSkew: 1, TopologyKey: corev1.LabelTopologyZone, WhenUnsatisfiable: when,
		LabelSelector: &metav1.LabelSelector{MatchLabels: sel},
	}}
}

// haNode is a Ready, schedulable node, labelled with zone unless it is empty.
func haNode(name, zone string) *corev1.Node {
	n := makeNode(name, true, 0)
	n.Labels = map[string]string{corev1.LabelHostname: name}
	if zone != "" {
		n.Labels[corev1.LabelTopologyZone] = zone
	}
	return n
}

// newHAFixture builds the install for mode, with the external-component and
// OpenBao-namespace settings cleared for the test.
func newHAFixture(t *testing.T, mode HAMode) *haFixture {
	t.Helper()
	t.Setenv(externalComponentsEnv, "")
	t.Setenv(openBaoNamespaceEnv, "")
	f := &haFixture{
		mode:    mode,
		nodes:   []*corev1.Node{haNode("node-a", "zone-a"), haNode("node-b", "zone-b"), haNode("node-c", "zone-c")},
		deploys: map[string]*appsv1.Deployment{}, sets: map[string]*appsv1.StatefulSet{},
		pdbs: map[string]*policyv1.PodDisruptionBudget{}, pods: map[string][]*corev1.Pod{},
	}
	for _, w := range haWorkloads {
		labels := haPodLabels(w)
		affinity, spread := haPlacement(mode, map[string]string{haInstanceLabel: w.release})
		tpl := corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{Affinity: affinity, TopologySpreadConstraints: spread}}
		ns, name := w.namespaceName(), haObjectName(w.release)
		switch w.tier {
		case haQuorum:
			f.addQuorum(w, ns, name, tpl)
		default:
			f.addDeployment(w, ns, name, tpl)
		}
	}
	return f
}

func (f *haFixture) addDeployment(w haWorkload, ns, name string, tpl corev1.PodTemplateSpec) {
	n := int32(2)
	strategy := appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
	if w.tier == haSingleton {
		n = 1
	} else {
		zero, one := intstr.FromInt32(0), intstr.FromInt32(1)
		strategy.RollingUpdate = &appsv1.RollingUpdateDeployment{MaxUnavailable: &zero, MaxSurge: &one}
		minAvailable := intstr.FromInt32(1)
		f.pdbs[w.release] = &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &minAvailable,
				Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(tpl.Labels)}},
		}
	}
	f.deploys[w.release] = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: appsv1.DeploymentSpec{Replicas: &n, Strategy: strategy, Template: tpl,
			Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(tpl.Labels)}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: n, UpdatedReplicas: n, ReadyReplicas: n,
			AvailableReplicas: n},
	}
	controller := true
	for i := range int(n) {
		labels := maps.Clone(tpl.Labels)
		labels[appsv1.DefaultDeploymentUniqueLabelKey] = "abc12"
		f.pods[w.release] = append(f.pods[w.release], &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-abc12-%d", name, i), Namespace: ns, Labels: labels,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet",
					Name: name + "-abc12", UID: types.UID("rs-" + name), Controller: &controller}}},
			Spec:   corev1.PodSpec{NodeName: f.nodes[i%len(f.nodes)].Name},
			Status: readyPodStatus(true),
		})
	}
}

func (f *haFixture) addQuorum(w haWorkload, ns, name string, tpl corev1.PodTemplateSpec) {
	n := int32(3)
	uid := types.UID("sts-" + name)
	f.sets[w.release] = &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid, Generation: 1},
		Spec: appsv1.StatefulSetSpec{Replicas: &n, Template: tpl,
			Selector:       &metav1.LabelSelector{MatchLabels: maps.Clone(tpl.Labels)},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, Replicas: n, ReadyReplicas: n, UpdatedReplicas: n,
			CurrentRevision: name + "-r1", UpdateRevision: name + "-r1"},
	}
	maxUnavailable := intstr.FromInt32(1)
	f.pdbs[w.release] = &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &maxUnavailable,
			Selector: &metav1.LabelSelector{MatchLabels: maps.Clone(tpl.Labels)}},
	}
	controller := true
	for i := range int(n) {
		f.pods[w.release] = append(f.pods[w.release], &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", name, i), Namespace: ns,
				Labels: maps.Clone(tpl.Labels),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: name,
					UID: uid, Controller: &controller}}},
			Spec:   corev1.PodSpec{NodeName: f.nodes[i%len(f.nodes)].Name},
			Status: readyPodStatus(true),
		})
	}
}

// remove drops release's workload, PDB and pods, as an install without it.
func (f *haFixture) remove(release string) {
	delete(f.deploys, release)
	delete(f.sets, release)
	delete(f.pdbs, release)
	delete(f.pods, release)
}

// place puts release's pods, in order, on nodes.
func (f *haFixture) place(release string, nodes ...string) {
	for i, p := range f.pods[release] {
		p.Spec.NodeName = nodes[i]
	}
}

// template is the pod template of release's workload.
func (f *haFixture) template(release string) *corev1.PodTemplateSpec {
	if d, ok := f.deploys[release]; ok {
		return &d.Spec.Template
	}
	return &f.sets[release].Spec.Template
}

func (f *haFixture) objects() []runtime.Object {
	var objs []runtime.Object
	for _, n := range f.nodes {
		objs = append(objs, n)
	}
	for _, d := range f.deploys {
		objs = append(objs, d)
	}
	for _, s := range f.sets {
		objs = append(objs, s)
	}
	for _, p := range f.pdbs {
		objs = append(objs, p)
	}
	for _, pods := range f.pods {
		for _, p := range pods {
			objs = append(objs, p)
		}
	}
	return append(objs, f.extra...)
}

// run runs the HA Guarantee on the fixture, after install unless preInstall,
// with react applied to its client first, and returns the state and log.
func (f *haFixture) run(t *testing.T, preInstall bool, react func(*fake.Clientset)) (*ValidationState, string) {
	t.Helper()
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	l.SetFormatter(&CLIFormatter{})
	state := &ValidationState{Log: logrus.NewEntry(l), HAMode: f.mode, PostInstall: !preInstall}
	client := fake.NewSimpleClientset(f.objects()...)
	if react != nil {
		react(client)
	}
	checkHAGuarantee(context.Background(), client, state)
	return state, buf.String()
}
