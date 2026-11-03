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

package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func TestStartRootfsCapture_DisabledIsNoop(t *testing.T) {
	a := &Agent{config: Config{}, log: logrus.New()}
	if _, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{Enabled: false}); err != nil {
		t.Fatalf("disabled should be no-op; got %v", err)
	}
}

func TestStartRootfsCapture_EnabledFailsWithoutKubeConfig(t *testing.T) {
	// In a unit-test environment with no in-cluster SA + no KUBECONFIG,
	// kube client construction fails. We verify that error is surfaced
	// (so a misconfigured agent doesn't silently skip capture).
	t.Setenv("KUBECONFIG", "/nonexistent/kubeconfig")
	t.Setenv("HOME", t.TempDir()) // hide any ~/.kube/config the test runner has
	a := &Agent{config: Config{}, log: logrus.New()}
	// PodCacheDir is set so the whole-rootfs guard does not answer first.
	// Without it this test would still fail -- but on the guard, not on the
	// kube client it is named for, and the coverage would be gone silently.
	_, err := a.startRootfsCapture(context.Background(), RootfsCaptureConfig{
		Enabled:     true,
		PodCacheDir: "/opt/nvsnap",
	})
	if err == nil {
		t.Fatal("expected kube client construction error when no config available")
	}
	if strings.Contains(err.Error(), "whole-rootfs") {
		t.Fatalf("guard fired instead of the kube client path; this test no longer covers what it claims: %v", err)
	}
	// Assert positively, not just that some error occurred. Any new early
	// return -- another guard, a validation, a typo'd default -- would satisfy
	// "err != nil" while leaving buildKubeClient uncovered, and the test would
	// keep passing under a name that no longer describes it.
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "kube") && !strings.Contains(msg, "cluster") && !strings.Contains(msg, "config") {
		t.Fatalf("expected a kube client construction failure, got something else: %v", err)
	}
}

// The capture identity folds in the operator's cache salt and the tenant,
// and every caller gets the same composer: a capture the watcher files
// without an election stamp lands under the hash the webhook looks up.
func TestHashComposer(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "vllm/vllm-openai:v1", Args: []string{"serve", "m"}}}},
	}
	hash := func(a *Agent) string { return checkpointstore.ComputeHash(a.hashComposer().Compose(pod, 0)) }
	plain := &Agent{}
	salted := &Agent{config: Config{Webhook: WebhookConfig{CacheSalt: "s1"}}}
	if hash(plain) == hash(salted) {
		t.Error("the cache salt does not change the capture identity")
	}
	if hash(salted) != hash(&Agent{config: Config{Webhook: WebhookConfig{CacheSalt: "s1"}}}) {
		t.Error("one configuration composes two identities")
	}
	if got := plain.hashComposer().CUDADriverMajor; got != 580 {
		t.Errorf("driver major %d without configuration, want 580", got)
	}
	if got := plain.hashComposer().Compose(pod, 0).Tenant; got != "namespace:ns" {
		t.Errorf("tenant %q without a tenant label", got)
	}
}
