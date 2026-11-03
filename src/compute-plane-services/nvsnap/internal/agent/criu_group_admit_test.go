// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// A group restore needs every rank to arrive as a placeholder together;
// anything else starts cold.
func TestCRIUGroupAdmits(t *testing.T) {
	yes := true
	sts := func(policy appsv1.PodManagementPolicyType) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "engine", Namespace: "fn", UID: "sts-uid"},
			Spec: appsv1.StatefulSetSpec{PodManagementPolicy: policy}}
	}
	rank := func(name string, ann map[string]string, deleting bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fn", Annotations: ann,
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "engine", UID: types.UID("sts-uid"), Controller: &yes}}}}
		if deleting {
			now := metav1.Now()
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"test"}
		}
		return p
	}
	placeholder := map[string]string{webhook.CRIUGroupAnnotation: "g"}
	restored := map[string]string{webhook.CRIUGroupAnnotation: "g", webhook.CRIURestoredAnnotation: "true"}

	for _, tc := range []struct {
		name   string
		objs   []runtime.Object
		pod    *corev1.Pod
		want   bool
		reason string
	}{
		{"whole instance created together", []runtime.Object{sts(appsv1.ParallelPodManagement)}, rank("engine-1", nil, false), true, ""},
		{"the other rank is a placeholder of the same restore", []runtime.Object{sts(appsv1.ParallelPodManagement), rank("engine-0", placeholder, false)}, rank("engine-1", nil, false), true, ""},
		{"the other rank is being deleted", []runtime.Object{sts(appsv1.ParallelPodManagement), rank("engine-0", nil, true)}, rank("engine-1", nil, false), true, ""},
		{"rank 1 replaced while rank 0 serves cold", []runtime.Object{sts(appsv1.ParallelPodManagement), rank("engine-0", nil, false)}, rank("engine-1", nil, false), false, "already runs"},
		{"rank 1 replaced while rank 0 serves restored", []runtime.Object{sts(appsv1.ParallelPodManagement), rank("engine-0", restored, false)}, rank("engine-1", nil, false), false, "already runs"},
		{"ordered StatefulSet", []runtime.Object{sts(appsv1.OrderedReadyPodManagement)}, rank("engine-0", nil, false), false, "in order"},
		{"StatefulSet policy unset means ordered", []runtime.Object{sts("")}, rank("engine-0", nil, false), false, "in order"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kc := fake.NewSimpleClientset(tc.objs...)
			a := &Agent{kubeClient: kc, log: logrus.New()}
			got, why := a.criuGroupAdmits(context.Background(), tc.pod)
			if got != tc.want || !strings.Contains(why, tc.reason) {
				t.Errorf("admits = %v (%q), want %v (%q)", got, why, tc.want, tc.reason)
			}
		})
	}

	// A LeaderWorkerSet creates its ranks itself; the StatefulSet it uses
	// underneath is not consulted.
	lws := rank("engine-0", nil, false)
	lws.Labels = map[string]string{lwsGroupKeyLabel: "grp"}
	a := &Agent{kubeClient: fake.NewSimpleClientset(sts(appsv1.OrderedReadyPodManagement)), log: logrus.New()}
	if ok, why := a.criuGroupAdmits(context.Background(), lws); !ok {
		t.Errorf("LWS rank refused: %s", why)
	}
}
