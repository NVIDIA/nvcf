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

// Tests for waitForRWXDetach — the durability barrier before the L2
// block-level snapshot (nvsnap#105). A snapshot taken before the rwx
// volume detaches (and its unmount flushes to disk) captures truncated
// files, poisoning the rox and every fan-out pod.

package checkpointstore

import (
	"context"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func detachTestBackend(objs ...runtime.Object) *PerCapturePVCBackend {
	k := kubefake.NewSimpleClientset(objs...)
	b := &PerCapturePVCBackend{
		KubeClient:    k,
		Namespace:     "nvsnap-system",
		Log:           logrus.NewEntry(logrus.New()),
		DetachTimeout: 300 * time.Millisecond, // keep tests fast
	}
	return b
}

func boundRWXPVC(name, pvName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "nvsnap-system"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pvName},
	}
}

func attachmentFor(pvName string) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: "va-" + pvName},
		Spec: storagev1.VolumeAttachmentSpec{
			Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName},
		},
	}
}

// Still-attached → the barrier must NOT pass (it times out rather than
// letting the snapshot run against an unflushed volume).
func TestWaitForRWXDetach_BlocksWhileAttached(t *testing.T) {
	b := detachTestBackend(boundRWXPVC("rwx-abc", "pv-abc"), attachmentFor("pv-abc"))
	err := b.waitForRWXDetach(context.Background(), "nvsnap-system", "rwx-abc")
	if err == nil {
		t.Fatal("expected timeout error while the rwx volume is still attached, got nil")
	}
}

// No VolumeAttachment for the PV → detached → barrier passes.
func TestWaitForRWXDetach_PassesWhenDetached(t *testing.T) {
	b := detachTestBackend(boundRWXPVC("rwx-abc", "pv-abc"), attachmentFor("pv-other"))
	if err := b.waitForRWXDetach(context.Background(), "nvsnap-system", "rwx-abc"); err != nil {
		t.Fatalf("expected nil when the rwx PV has no attachment, got %v", err)
	}
}

// Unbound rwx PVC (never attached) → nothing to flush → barrier passes.
func TestWaitForRWXDetach_UnboundPVCSkips(t *testing.T) {
	b := detachTestBackend(boundRWXPVC("rwx-abc", ""))
	if err := b.waitForRWXDetach(context.Background(), "nvsnap-system", "rwx-abc"); err != nil {
		t.Fatalf("expected nil for an unbound rwx PVC, got %v", err)
	}
}
