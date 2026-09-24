// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func scWithProvisioner(name, provisioner string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: provisioner,
	}
}

// An unqualified provisioner must be reported as such, not silently
// promoted on the Hyperdisk-ML shape. Weka is the live example: it is in
// no built-in profile, and assuming cross-node ReadOnlyMany on it can
// bind a claim that reads wrong.
func TestResolveL2Promoter_UnqualifiedProvisionerFailsClosed(t *testing.T) {
	kc := fake.NewSimpleClientset(scWithProvisioner("weka-sc", "csi.weka.io"))
	_, err := resolveL2Promoter(context.Background(), kc, nil, "weka-sc", "nvsnap-system", quietLog())
	if !errors.Is(err, errUnqualifiedStorage) {
		t.Fatalf("want errUnqualifiedStorage for an unknown provisioner, got %v", err)
	}
}

// An unreadable StorageClass is not a qualified one either.
func TestResolveL2Promoter_MissingStorageClassFailsClosed(t *testing.T) {
	kc := fake.NewSimpleClientset()
	_, err := resolveL2Promoter(context.Background(), kc, nil, "absent-sc", "nvsnap-system", quietLog())
	if !errors.Is(err, errUnqualifiedStorage) {
		t.Fatalf("want errUnqualifiedStorage for an unreadable class, got %v", err)
	}
}

// A qualified shared-volume provisioner still resolves, so failing closed
// has not broken the backends that were already supported.
func TestResolveL2Promoter_QualifiedProvisionerStillResolves(t *testing.T) {
	kc := fake.NewSimpleClientset(scWithProvisioner("nvmesh-sc", "nvmesh-csi.excelero.com"))
	p, err := resolveL2Promoter(context.Background(), kc, nil, "nvmesh-sc", "nvsnap-system", quietLog())
	if err != nil {
		t.Fatalf("qualified provisioner should resolve, got %v", err)
	}
	if p == nil {
		t.Fatal("qualified provisioner returned a nil promoter")
	}
}
