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

package miniservice

import (
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nvcatypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// WorkerServiceAccountName is the fixed ServiceAccount that NVCF worker pods run as.
	// Each MiniService instance has its own namespace, so the name carries no instance suffix;
	// ICMS anchors worker identity to the namespace, the ServiceAccount UID, and the pod UID.
	WorkerServiceAccountName = "nvcf-worker"
	// WorkerServiceAccountPrefix is the legacy per-instance naming prefix. It is still reserved so
	// workload charts cannot use it.
	WorkerServiceAccountPrefix = "nvcf-worker-"
)

// WorkerSubject returns the Kubernetes subject of the worker ServiceAccount in namespace.
func WorkerSubject(namespace string) string {
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, WorkerServiceAccountName)
}

// IsWorkerServiceAccountName reports whether name is reserved for worker identity.
func IsWorkerServiceAccountName(name string) bool {
	return name == WorkerServiceAccountName || strings.HasPrefix(name, WorkerServiceAccountPrefix)
}

// ValidateWorkloadObject rejects a workload (Helm chart) object that could assume or alter the
// worker identity: a pod template running as the worker ServiceAccount, an identity object that
// reuses its name, a RoleBinding that grants it permissions, or any object claiming to be NVCA
// infrastructure. Kubernetes RBAC cannot express this restriction because it authorizes the
// creator of a Pod, not the ServiceAccount the Pod runs as.
func ValidateWorkloadObject(obj client.Object) error {
	if obj == nil {
		return nil
	}
	if nvcatypes.IsInfraOwnedObject(obj) {
		return fmt.Errorf("workload object %s/%s may not carry the %s annotation",
			obj.GetNamespace(), obj.GetName(), nvcatypes.InfraObjectAnnotationKey)
	}

	var ps *corev1.PodSpec
	switch t := obj.(type) {
	case *corev1.Pod:
		ps = &t.Spec
	case *appsv1.Deployment:
		ps = &t.Spec.Template.Spec
	case *appsv1.ReplicaSet:
		ps = &t.Spec.Template.Spec
	case *appsv1.StatefulSet:
		ps = &t.Spec.Template.Spec
	case *appsv1.DaemonSet:
		ps = &t.Spec.Template.Spec
	case *batchv1.Job:
		ps = &t.Spec.Template.Spec
	case *batchv1.CronJob:
		ps = &t.Spec.JobTemplate.Spec.Template.Spec
	case *corev1.ServiceAccount:
		if IsWorkerServiceAccountName(t.Name) {
			return fmt.Errorf("workload ServiceAccount %q uses a reserved worker identity name", t.Name)
		}
		return nil
	case *rbacv1.Role:
		if IsWorkerServiceAccountName(t.Name) {
			return fmt.Errorf("workload Role %q uses a reserved worker identity name", t.Name)
		}
		return nil
	case *rbacv1.RoleBinding:
		if IsWorkerServiceAccountName(t.Name) {
			return fmt.Errorf("workload RoleBinding %q uses a reserved worker identity name", t.Name)
		}
		for _, s := range t.Subjects {
			if s.Kind == rbacv1.ServiceAccountKind && IsWorkerServiceAccountName(s.Name) {
				return fmt.Errorf("workload RoleBinding %q grants permissions to worker ServiceAccount %q", t.Name, s.Name)
			}
		}
		return nil
	default:
		return nil
	}

	if IsWorkerServiceAccountName(ps.ServiceAccountName) {
		return fmt.Errorf("workload pods may not use worker ServiceAccount %q", ps.ServiceAccountName)
	}
	return nil
}

// ValidateWorkloadObjects applies ValidateWorkloadObject to every object and joins the errors.
func ValidateWorkloadObjects(objs []client.Object) error {
	var errs []error
	for _, obj := range objs {
		if err := ValidateWorkloadObject(obj); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// BuildWorkerAuth builds the worker identity registration NVCA reports to ICMS for a MiniService
// instance. Only pods NVCA authored are included: they must carry the NVCA infra annotation, live
// in namespace, and run as the worker ServiceAccount. Selection is by ownership, never by
// ServiceAccount name or label alone, so a workload pod that binds the ServiceAccount is never
// registered. Returns nil when there is nothing to register.
func BuildWorkerAuth(namespace string, sa *corev1.ServiceAccount, pods ...*corev1.Pod) *nvcatypes.WorkerAuth {
	if sa == nil || sa.Name != WorkerServiceAccountName || sa.Namespace != namespace || sa.UID == "" {
		return nil
	}
	auth := &nvcatypes.WorkerAuth{
		Sub:       WorkerSubject(namespace),
		Namespace: namespace,
		SAuid:     string(sa.UID),
	}
	for _, p := range pods {
		if p == nil || p.Namespace != namespace || p.UID == "" ||
			p.Spec.ServiceAccountName != WorkerServiceAccountName || !nvcatypes.IsInfraOwnedObject(p) {
			continue
		}
		auth.WorkerIdentifiers = append(auth.WorkerIdentifiers, nvcatypes.WorkerIdentifier{
			Name: p.Name,
			UID:  string(p.UID),
		})
	}
	if len(auth.WorkerIdentifiers) == 0 {
		return nil
	}
	return auth
}
