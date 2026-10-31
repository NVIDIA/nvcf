// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReleaseIdleBinds(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"aaaa000000000000", "bbbb000000000000"} {
		if err := os.MkdirAll(filepath.Join(root, key), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reader := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "fn"},
		Spec: corev1.PodSpec{NodeName: "n1", Volumes: []corev1.Volume{{Name: "m",
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/lib/nvsnap/models/aaaa000000000000"}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	bound := map[string]bool{filepath.Join(root, "aaaa000000000000"): true, filepath.Join(root, "bbbb000000000000"): true}
	c := &ModelVolumeController{Kube: fake.NewSimpleClientset(reader), NodeName: "n1", HostRoot: root}
	c.init()
	c.mountedDevice = func(dst string) string {
		if bound[dst] {
			return "/dev/x"
		}
		return ""
	}
	c.unbind = func(dst string) error { delete(bound, dst); return nil }

	t0 := time.Now()
	c.releaseIdleBinds(context.Background(), t0)
	c.releaseIdleBinds(context.Background(), t0.Add(time.Minute))
	if !bound[filepath.Join(root, "bbbb000000000000")] {
		t.Fatal("an unused volume was unbound before the grace")
	}
	c.releaseIdleBinds(context.Background(), t0.Add(modelBindIdleGrace+time.Second))
	if bound[filepath.Join(root, "bbbb000000000000")] {
		t.Error("an unused volume stayed bound past the grace")
	}
	if !bound[filepath.Join(root, "aaaa000000000000")] {
		t.Error("a volume a running pod uses was unbound")
	}
}
