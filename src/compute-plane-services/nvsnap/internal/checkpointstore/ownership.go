// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package checkpointstore

// Who a promoted capture belongs to. A claim to it is minted only in
// namespaces of the same tenant: an explicit restore-from, or a pod
// labelled with the capture's hash, names the capture but proves nothing. The record
// lives in the backend's own namespace, which tenants cannot write; a label
// on the tenant's claim could be forged. The tenant is the one the capture's
// namespace had when it was promoted.

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ownerRecordName(hash string) string { return "nvsnap-owner-" + ShortHash(hash) }

// tenantOf names namespace ns's tenant; without a resolver every namespace
// is its own.
func (b *PerCapturePVCBackend) tenantOf(ctx context.Context, ns string) string {
	if b.TenantOf != nil {
		return b.TenantOf(ctx, ns)
	}
	return "namespace:" + ns
}

// recordOwner records the tenant of the capture of hash, promoted from
// namespace ns. The first promote of a hash owns it: a later one from
// another tenant (whose hash would differ unless forged) does not take it
// over.
func (b *PerCapturePVCBackend) recordOwner(ctx context.Context, hash, ns string) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ownerRecordName(hash),
			Namespace: b.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "nvsnap", "nvsnap.io/kind": "owner"},
		},
		Data: map[string]string{"hash": hash, "tenant": b.tenantOf(ctx, ns), "namespace": ns},
	}
	_, err := b.KubeClient.CoreV1().ConfigMaps(b.Namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		b.log().WithError(err).WithField("hash", ShortHash(hash)).Warn("cannot record the capture's tenant; claims in other namespaces are refused")
	}
}

// mayClaim reports whether a claim to hash may be minted in namespace ns:
// its tenant must be the one recorded for the capture. A capture with no
// record (promoted before records existed) is refused: what is not known
// to be shared is not shared, and the namespace starts cold.
func (b *PerCapturePVCBackend) mayClaim(ctx context.Context, hash, ns string) bool {
	why := ""
	cm, err := b.KubeClient.CoreV1().ConfigMaps(b.Namespace).Get(ctx, ownerRecordName(hash), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		why = "no owner record"
	case err != nil:
		why = fmt.Sprintf("owner record unreadable: %v", err)
	case cm.Data["hash"] != hash:
		why = "owner record names another hash"
	default:
		if t := b.tenantOf(ctx, ns); t != cm.Data["tenant"] {
			why = "namespace's tenant " + t + " is not the capture's " + cm.Data["tenant"]
		}
	}
	if why == "" {
		return true
	}
	b.log().WithFields(logrus.Fields{"hash": ShortHash(hash), "namespace": ns, "reason": why}).
		Warn("claim to a capture refused for a namespace outside its tenant")
	return false
}
