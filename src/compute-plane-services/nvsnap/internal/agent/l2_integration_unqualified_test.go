// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

func scWithProvisioner(name, provisioner string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:  metav1.ObjectMeta{Name: name},
		Provisioner: provisioner,
	}
}

func scWithType(name, provisioner, volType string) *storagev1.StorageClass {
	sc := scWithProvisioner(name, provisioner)
	sc.Parameters = map[string]string{"type": volType}
	return sc
}

func profilesCM(ns, body string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: storageProfilesConfigMap, Namespace: ns},
		Data:       map[string]string{"profiles.yaml": body},
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

// The remedy the error message tells operators to use has to actually
// work: a ConfigMap entry must qualify a provisioner the built-in table
// has never heard of. Untested until now, which is poor for advice we
// print in an error.
func TestResolveL2Promoter_ConfigMapQualifiesUnknownProvisioner(t *testing.T) {
	kc := fake.NewSimpleClientset(
		scWithProvisioner("weka-sc", "csi.weka.io"),
		profilesCM("nvsnap-system", "csi.weka.io:\n  strategy: shared-volume\n  volumeHandleTransform: none\n"),
	)
	p, err := resolveL2Promoter(context.Background(), kc, nil, "weka-sc", "nvsnap-system", quietLog())
	if err != nil {
		t.Fatalf("ConfigMap-qualified provisioner should resolve, got %v", err)
	}
	if p == nil {
		t.Fatal("ConfigMap-qualified provisioner returned a nil promoter")
	}
}

// A ConfigMap entry outranks the built-in for the same key, so an operator
// can correct a built-in that is wrong for their cluster.
func TestResolveL2Promoter_ConfigMapOverridesBuiltin(t *testing.T) {
	kc := fake.NewSimpleClientset(
		scWithProvisioner("nvmesh-sc", "nvmesh-csi.excelero.com"),
		profilesCM("nvsnap-system", "nvmesh-csi.excelero.com:\n  strategy: snapshot-clone\n  snapshotClass: some-class\n  readOnlyMany: true\n"),
	)
	p, err := resolveL2Promoter(context.Background(), kc, nil, "nvmesh-sc", "nvsnap-system", quietLog())
	if err != nil {
		t.Fatalf("override should resolve, got %v", err)
	}
	if got := p.Caps().Strategy; got != checkpointstore.StrategySnapshotClone {
		t.Fatalf("ConfigMap override ignored: strategy = %q, want snapshot-clone", got)
	}
}

// A malformed ConfigMap must not become a silent qualification. The
// overlay is dropped and the driver is then unqualified, so L2 goes off
// rather than promoting on the shape the operator was trying to set.
func TestResolveL2Promoter_BadConfigMapYAMLStillFailsClosed(t *testing.T) {
	kc := fake.NewSimpleClientset(
		scWithProvisioner("weka-sc", "csi.weka.io"),
		profilesCM("nvsnap-system", "csi.weka.io: [this is not a profile"),
	)
	if _, err := resolveL2Promoter(context.Background(), kc, nil, "weka-sc", "nvsnap-system", quietLog()); !errors.Is(err, errUnqualifiedStorage) {
		t.Fatalf("bad ConfigMap should leave the driver unqualified, got %v", err)
	}
}

// No ConfigMap at all is the common case and must not be an error by
// itself: the built-in table still qualifies the backends it knows.
func TestResolveL2Promoter_AbsentConfigMapUsesBuiltins(t *testing.T) {
	kc := fake.NewSimpleClientset(scWithProvisioner("nvmesh-sc", "nvmesh-csi.excelero.com"))
	if _, err := resolveL2Promoter(context.Background(), kc, nil, "nvmesh-sc", "nvsnap-system", quietLog()); err != nil {
		t.Fatalf("absent ConfigMap should fall through to built-ins, got %v", err)
	}
}

// parameters.type disambiguates one provisioner that multiplexes volume
// types. hyperdisk-ml qualifies cross-node ReadOnlyMany; pd-ssd does not,
// and the two must not be confused since that is the exact assumption
// this change exists to stop making.
func TestResolveL2Promoter_CompositeKeyDistinguishesVolumeType(t *testing.T) {
	kcML := fake.NewSimpleClientset(scWithType("hdml", "pd.csi.storage.gke.io", "hyperdisk-ml"))
	pML, err := resolveL2Promoter(context.Background(), kcML, nil, "hdml", "nvsnap-system", quietLog())
	if err != nil {
		t.Fatalf("hyperdisk-ml should resolve, got %v", err)
	}
	kcSSD := fake.NewSimpleClientset(scWithType("ssd", "pd.csi.storage.gke.io", "pd-ssd"))
	pSSD, err := resolveL2Promoter(context.Background(), kcSSD, nil, "ssd", "nvsnap-system", quietLog())
	if err != nil {
		t.Fatalf("pd-ssd should resolve, got %v", err)
	}
	if pML.Caps().ReadOnlyMany == pSSD.Caps().ReadOnlyMany {
		t.Fatalf("hyperdisk-ml and pd-ssd resolved to the same ReadOnlyMany (%v); the composite key is not discriminating",
			pML.Caps().ReadOnlyMany)
	}
}
