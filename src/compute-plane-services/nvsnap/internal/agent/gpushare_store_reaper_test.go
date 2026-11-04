// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestReapGPUShareStores(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, GPUSharePodStoresSubdir)
	old := time.Now().Add(-time.Hour)
	for _, uid := range []string{"live", "gone", "new"} {
		if err := os.MkdirAll(filepath.Join(root, uid), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, uid, "chunk"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if uid != "new" {
			if err := os.Chtimes(filepath.Join(root, uid), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	kc := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", UID: "live"}, Spec: corev1.PodSpec{NodeName: "n1"}})
	a := &Agent{kubeClient: kc, log: logrus.New(), config: Config{NodeName: "n1", CheckpointDir: dir}}
	a.reapNode(context.Background(), time.Now())
	for uid, want := range map[string]bool{"live": true, "gone": false, "new": true} {
		_, err := os.Stat(filepath.Join(root, uid))
		if got := err == nil; got != want {
			t.Errorf("store %s kept = %v, want %v", uid, got, want)
		}
	}
}
