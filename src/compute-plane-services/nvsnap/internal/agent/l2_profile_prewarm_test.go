// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The resolver returns the profile next to the promoter so the webhook can
// read the prewarm policy, and a nvsnap-storage-profiles ConfigMap entry
// that turns the prewarm off for a provisioner reaches it unchanged.
func TestResolveL2Promoter_ReturnsProfileWithPrewarmPolicy(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)
	sc := &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: "nvcf-sc"},
		Provisioner: "nvmesh-csi.excelero.com",
	}

	kc := fake.NewSimpleClientset(sc)
	promoter, profile, err := resolveL2Promoter(context.Background(), kc, nil, "nvcf-sc", "nvsnap-system", log)
	if err != nil {
		t.Fatalf("built-in NVMesh profile: %v", err)
	}
	if promoter == nil || profile == nil {
		t.Fatalf("built-in NVMesh profile: promoter=%v profile=%v, want both", promoter, profile)
	}
	if !profile.PrewarmEnabled() || profile.PrewarmWorkers() != 8 {
		t.Errorf("built-in NVMesh profile ships prewarm on with 8 readers, got enabled=%v workers=%d", profile.PrewarmEnabled(), profile.PrewarmWorkers())
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: storageProfilesConfigMap, Namespace: "nvsnap-system"},
		Data: map[string]string{"profiles.yaml": `
nvmesh-csi.excelero.com:
  strategy: shared-volume
  volumeHandleTransform: nvmesh
  mountOptions: [ro, norecovery, nouuid]
  prewarm: false
  prewarmParallelism: 2
`},
	}
	kc = fake.NewSimpleClientset(sc, cm)
	promoter, profile, err = resolveL2Promoter(context.Background(), kc, nil, "nvcf-sc", "nvsnap-system", log)
	if err != nil {
		t.Fatalf("ConfigMap overlay: %v", err)
	}
	if promoter == nil || profile == nil {
		t.Fatalf("ConfigMap overlay: promoter=%v profile=%v, want both", promoter, profile)
	}
	if profile.PrewarmEnabled() || profile.PrewarmWorkers() != 2 {
		t.Errorf("ConfigMap prewarm policy lost on the way to the webhook: enabled=%v workers=%d, want off/2 (overlay flips the NVMesh default)", profile.PrewarmEnabled(), profile.PrewarmWorkers())
	}

	// No match: L2 is disabled and there is nothing to hand the webhook,
	// so it falls back to defaults.
	unknown := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "weka"}, Provisioner: "csi.weka.io"}
	_, profile, err = resolveL2Promoter(context.Background(), fake.NewSimpleClientset(unknown), nil, "weka", "nvsnap-system", log)
	if !errors.Is(err, errUnqualifiedStorage) {
		t.Errorf("unmatched provisioner: err=%v, want errUnqualifiedStorage", err)
	}
	if profile != nil {
		t.Errorf("unmatched provisioner must yield a nil profile, got %+v", profile)
	}
}
