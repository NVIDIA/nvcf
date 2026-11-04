// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// A pod without GPUs is a capture candidate (the webhook widens its liveness
// probes) only when it or its namespace is opted into capture: the
// companions of an opted-in instance, never an unrelated service.
func TestCaptureCandidate(t *testing.T) {
	ns := func(name string, labels map[string]string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}
	kc := fake.NewSimpleClientset(
		ns("cc-plain", nil),
		ns("cc-function", map[string]string{"function-id": "fn-1"}),
		ns("cc-other-function", map[string]string{"function-id": "fn-2"}),
		ns("cc-labelled", map[string]string{criuCaptureAnnotation: "true"}),
	)
	a := &Agent{kubeClient: kc, log: logrus.New(),
		config: Config{CaptureOptIn: []CaptureOptIn{{Label: "function-id", Values: []string{"fn-1"}}}}}
	pod := func(namespace string, annotations, labels map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: namespace, Annotations: annotations, Labels: labels}}
	}
	ctx := context.Background()
	for _, c := range []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{"service of a namespace no capture covers", pod("cc-plain", nil, nil), false},
		{"pod annotated for capture", pod("cc-plain", map[string]string{criuCaptureAnnotation: "true"}, nil), true},
		{"pod with an opted-in label", pod("cc-plain", nil, map[string]string{"function-id": "fn-1"}), true},
		{"companion in an opted-in function's namespace", pod("cc-function", nil, nil), true},
		{"pod of a function not opted in", pod("cc-other-function", nil, nil), false},
		{"companion in a namespace labelled for capture", pod("cc-labelled", nil, nil), true},
		{"namespace that cannot be read", pod("cc-missing", nil, nil), false},
	} {
		if got := a.captureCandidate(ctx, c.pod); got != c.want {
			t.Errorf("%s: candidate %v, want %v", c.name, got, c.want)
		}
	}
}
