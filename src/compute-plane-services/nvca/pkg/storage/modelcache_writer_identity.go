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

package storage

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// ApplySharedClaimWriterIdentity gives the shared-claim writer Job the
// identity the persisted selection recorded. Only the root identity changes
// the Job; fsGroup and an absent identity (a selection persisted before the
// field existed) keep whatever the translator emitted. Both the regular
// workflow and the Helm shared-filesystem path call it, so a driver that
// skips fsGroup on ReadWriteMany claims gets the same writer either way.
func ApplySharedClaimWriterIdentity(job *batchv1.Job, identity string) {
	if identity == ModelCacheWriterIdentityRoot {
		RunSharedClaimWriterAsRoot(job)
	}
}

// RunSharedClaimWriterAsRoot makes the containers that write the shared
// (ReadWriteMany) claim run as root. It is applied only when the persisted
// selection recorded the root writer identity, which the storage resolver
// sets from the live CSIDriver or an explicit catalog entry: the translator
// relies on the pod fsGroup to make the volume writable by the non-root
// writer, but Kubernetes applies fsGroup only under the File fsGroupPolicy.
// Drivers that declare ReadWriteOnceWithFSType, as OCI FSS and a default
// NetApp Trident install do, skip ReadWriteMany volumes, so a fresh claim's
// root stays owned by root and the writer fails on its first mkdir with
// "permission denied". Drivers that apply fsGroup, as Weka does by default,
// and selections persisted before the identity existed keep the non-root
// writer. There is exactly one writer per handle, it only populates the cache,
// and it writes world-readable files, so running it as root where required
// keeps the read-only readers unprivileged.
//
// Only containers with a read-write mount of the claim are changed, init
// containers included; sidecars and other containers keep their identity.
// RunAsNonRoot is set to false explicitly: the container value is what
// overrides a pod-level runAsNonRoot of true, which would otherwise make the
// kubelet refuse to start a uid 0 container.
func RunSharedClaimWriterAsRoot(job *batchv1.Job) {
	root := int64(0)
	notRequired := false
	spec := &job.Spec.Template.Spec
	for _, list := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range list {
			c := &list[i]
			if !MountsSharedClaimReadWrite(c) {
				continue
			}
			if c.SecurityContext == nil {
				c.SecurityContext = &corev1.SecurityContext{}
			}
			c.SecurityContext.RunAsUser = &root
			c.SecurityContext.RunAsGroup = &root
			c.SecurityContext.RunAsNonRoot = &notRequired
		}
	}
}

// MountsSharedClaimReadWrite reports whether a container mounts the model
// volume read-write, which is what makes it a writer of the shared claim.
func MountsSharedClaimReadWrite(c *corev1.Container) bool {
	for _, m := range c.VolumeMounts {
		if m.Name == ModelCachePodVolumeName && !m.ReadOnly {
			return true
		}
	}
	return false
}

// sharedClaimWriterIdentityFromRequest reads the writer identity the agent
// persisted on the request when it selected storage. A request without a
// selection, or one persisted before the field existed, yields "" so the
// writer keeps the translator's identity.
func sharedClaimWriterIdentityFromRequest(annotations map[string]string) (string, error) {
	raw := annotations[ModelCacheStorageSelectionAnnotationKey]
	if raw == "" {
		return "", nil
	}
	selection, err := ParsePersistedModelCacheStorageSelection(raw)
	if err != nil {
		return "", err
	}
	return selection.WriterIdentity, nil
}
