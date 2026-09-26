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

// Package modelvolume owns the per-identity model volume of
// docs/proposals/helm-shared-model-volume.md: one volume per model URI per
// cluster, written once by the elected writer's download step, immutable
// afterwards, attached by every other pod.
//
// Two modes, chosen by the storage profile:
//
//   - ModeRWX (distributed filesystem): one ReadWriteMany claim per
//     identity per namespace, mounted by writer and readers alike at
//     admission. Completion is a marker file the writer's download step
//     leaves at the volume root.
//   - ModeBlock (NVMesh): the writer's ReadWriteOnce claim is the artifact.
//     When its download step exits 0 the agent marks the claim complete and
//     mints the read-only secondary PV and claim; readers admitted before
//     that keep an emptyDir and wait for the agent to bind the volume in.
package modelvolume

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Mode is how the volume is shared.
type Mode string

// Modes: RWX on a distributed filesystem, Block on NVMesh.
const (
	ModeRWX   Mode = "rwx"
	ModeBlock Mode = "block"
)

// Labels and annotations on volumes and pods.
const (
	// IdentityLabel is the short identity key on claims, PVs and pods.
	IdentityLabel = "nvsnap.io/model"
	// IdentityAnnotation is the full model URI.
	IdentityAnnotation = "nvsnap.io/model-uri"
	// RoleLabel is writer | reader on pods.
	RoleLabel = "nvsnap.io/model-role"
	// CompleteLabel is "true" on the writer claim once the download exited 0.
	CompleteLabel = "nvsnap.io/model-complete"
	// PendingLabel is "true" on a Block-mode reader that still needs the
	// agent to bind the volume into its emptyDir.
	PendingLabel = "nvsnap.io/model-pending"
	// LandingAnnotation on a pod records the mount path the model must
	// appear at, for the agent's bind.
	LandingAnnotation = "nvsnap.io/model-landing"
	// MarkerFile at the volume root says the download completed.
	MarkerFile = ".nvsnap-complete"
	// DownloadInitAnnotation on a writer names the init container whose
	// exit 0 means the download completed.
	DownloadInitAnnotation = "nvsnap.io/model-download-init"

	managedBy = "nvsnap"
)

// Config comes from the storage profile.
type Config struct {
	Mode         Mode
	StorageClass string
	// Size requested for a new volume; the model size is unknown at
	// admission, so this is a ceiling. Thin-provisioned classes make it
	// cheap.
	Size resource.Quantity
}

// Key is the short stable token for a model URI, used in object names
// and labels (label values may be at most 63 characters).
func Key(uri string) string {
	sum := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(sum[:8])
}

// ClaimName is the writer claim in RWX mode and the shared claim in both.
func ClaimName(uri string) string { return "nvsnap-model-" + Key(uri) }

// ReadOnlyClaimName is the Block-mode read-only claim minted after completion.
func ReadOnlyClaimName(uri string) string { return "nvsnap-model-" + Key(uri) + "-ro" }

// ReadOnlyPVName is the static PV behind ReadOnlyClaimName in ns.
func ReadOnlyPVName(uri, ns string) string {
	sum := sha256.Sum256([]byte(ns))
	return "nvsnap-model-" + Key(uri) + "-ro-" + hex.EncodeToString(sum[:4])
}

// State of an identity on the cluster, as the webhook needs it.
type State struct {
	// Exists: a writer claim exists (a download is in flight or done).
	Exists bool
	// Complete: the download finished; readers may attach.
	Complete bool
	// ClaimNamespace is where the writer claim lives.
	ClaimNamespace string
}

// Provisioner creates and inspects model volumes.
type Provisioner struct {
	Kube kubernetes.Interface
	Cfg  Config
}

// EnsureWriterClaim creates the claim the writer downloads into, in ns.
// Idempotent. RWX mode creates a ReadWriteMany claim readers share; Block
// mode a ReadWriteOnce claim that becomes the read-only artifact.
func (p *Provisioner) EnsureWriterClaim(ctx context.Context, uri, ns string) (string, error) {
	name := ClaimName(uri)
	if _, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return name, nil
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get claim %s/%s: %w", ns, name, err)
	}
	mode := corev1.ReadWriteOnce
	if p.Cfg.Mode == ModeRWX {
		mode = corev1.ReadWriteMany
	}
	sc := p.Cfg.StorageClass
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": managedBy,
				IdentityLabel:                  Key(uri),
			},
			Annotations: map[string]string{IdentityAnnotation: uri},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{mode},
			StorageClassName: &sc,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: p.Cfg.Size}},
		},
	}
	if _, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create claim %s/%s: %w", ns, name, err)
	}
	return name, nil
}

// Lookup finds the writer claim for uri anywhere on the cluster and reports
// whether its download completed.
func (p *Provisioner) Lookup(ctx context.Context, uri string) (State, error) {
	list, err := p.Kube.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{LabelSelector: IdentityLabel + "=" + Key(uri)})
	if err != nil {
		return State{}, fmt.Errorf("list claims for %s: %w", uri, err)
	}
	st := State{}
	for i := range list.Items {
		c := &list.Items[i]
		if c.Name != ClaimName(uri) {
			continue
		}
		st.Exists = true
		st.ClaimNamespace = c.Namespace
		if c.Labels[CompleteLabel] == "true" {
			st.Complete = true
			return st, nil
		}
	}
	return st, nil
}

// MarkComplete labels the writer claim complete. The agent calls it when
// the writer's download step exits 0 (Block mode); in RWX mode the marker
// file is authoritative and this label is informational.
func (p *Provisioner) MarkComplete(ctx context.Context, uri, ns string) error {
	name := ClaimName(uri)
	pvc, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get claim %s/%s: %w", ns, name, err)
	}
	if pvc.Labels[CompleteLabel] == "true" {
		return nil
	}
	if pvc.Labels == nil {
		pvc.Labels = map[string]string{}
	}
	pvc.Labels[CompleteLabel] = "true"
	if _, err := p.Kube.CoreV1().PersistentVolumeClaims(ns).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("label claim %s/%s complete: %w", ns, name, err)
	}
	return nil
}
