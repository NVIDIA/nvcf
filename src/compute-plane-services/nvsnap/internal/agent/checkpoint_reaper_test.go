// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

func TestRetirableCheckpoints(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	write := func(id string, instance bool, age time.Duration) {
		md := CheckpointMetadata{ID: id, CreatedAt: now.Add(-age)}
		if instance {
			md.CatalogInfo = &CatalogInfo{InstanceCapture: true}
		}
		b, _ := json.Marshal(md)
		if err := os.MkdirAll(filepath.Join(dir, id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id, "metadata.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("recorded", true, 5*time.Hour)   // named by a complete record
	write("orphan", true, 5*time.Hour)     // a rank of a failed capture
	write("young-orphan", true, time.Hour) // its capture may still be running
	write("blocked", false, time.Minute)   // its restore failed
	write("blocked-in-use", false, 0)      // its restore failed, a pod still holds it
	write("plain", false, 100*time.Hour)   // an NVCA capture: not ours to judge
	if err := os.MkdirAll(filepath.Join(dir, GPUSharePodStoresSubdir), 0o755); err != nil {
		t.Fatal(err)
	}
	kc := fake.NewSimpleClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuRestoreFailedConfigMap, Namespace: "nvsnap-system"},
			Data: map[string]string{"blocked": "x", "blocked-in-use": "x"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: criuGroupConfigMapName("k"), Namespace: "nvsnap-system",
			Labels: map[string]string{"app.kubernetes.io/managed-by": "nvsnap"}},
			Data: map[string]string{"state": "complete", "size": "1", "checkpoint.0": "recorded"}},
	)
	a := &Agent{kubeClient: kc, log: logrus.New(), config: Config{NodeName: "n1", CheckpointDir: dir}}
	pods := []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p", Annotations: map[string]string{webhook.CRIURestoreAnnotation: "blocked-in-use"}}}}
	dead, err := a.retirableCheckpoints(context.Background(), pods, now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for id := range dead {
		got = append(got, id)
	}
	sort.Strings(got)
	if want := []string{"blocked", "orphan"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("retired %v, want %v", got, want)
	}
	a.reapCheckpoints(context.Background(), pods, now)
	if _, err := os.Stat(filepath.Join(dir, "orphan")); !os.IsNotExist(err) {
		t.Error("the orphaned rank was not removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "recorded")); err != nil {
		t.Error("a recorded checkpoint was removed")
	}
}
