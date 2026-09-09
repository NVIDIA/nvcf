/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package selfhosted

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var legacyControlPlaneNamespaces = []string{
	"api-keys",
	"cassandra-system",
	"ess",
	"nats-system",
	"ncp",
	"nvcf",
	"nvcf-backend",
	"nvcf-ui",
	"nvca-modelcache-init",
	"nvca-operator",
	"nvca-system",
	"sis",
	"vault-system",
}

var legacyNamespacedObjects = []objectRef{
	{Kind: "deployment", Namespace: "api-keys", Name: "api-keys"},
	{Kind: "deployment", Namespace: "ess", Name: "ess-api-deployment"},
	{Kind: "deployment", Namespace: "nvcf", Name: "grpc-proxy-deployment"},
	{Kind: "deployment", Namespace: "nvcf", Name: "invocation-service"},
	{Kind: "deployment", Namespace: "nvcf", Name: "notary-service"},
	{Kind: "deployment", Namespace: "nvcf", Name: "nvcf-api"},
	{Kind: "deployment", Namespace: "nvcf", Name: "ratelimiter"},
	{Kind: "deployment", Namespace: "nvcf", Name: "reval"},
	{Kind: "deployment", Namespace: "nvca-operator", Name: "nvca-operator"},
	{Kind: "deployment", Namespace: "sis", Name: "spot-instance-service"},
	{Kind: "statefulset", Namespace: "cassandra-system", Name: "cassandra"},
	{Kind: "statefulset", Namespace: "nats-system", Name: "nats"},
	{Kind: "statefulset", Namespace: "vault-system", Name: "openbao-server"},
}

var legacyClusterScopedObjects = []objectRef{
	{Kind: "clusterrole", Name: "nvca"},
	{Kind: "clusterrolebinding", Name: "nvca"},
	{Kind: "mutatingwebhookconfiguration", Name: "nvca"},
	{Kind: "validatingwebhookconfiguration", Name: "nvca"},
}

type objectRef struct {
	Kind      string
	Namespace string
	Name      string
}

func (r objectRef) String() string {
	if r.Namespace == "" {
		return r.Kind + "/" + r.Name
	}
	return r.Kind + "/" + r.Namespace + "/" + r.Name
}

// UnadoptedLegacyObjectsError is returned when a named control plane is being
// installed into a cluster that still has legacy NVCF objects without an owner
// label. The existing default plane must be upgraded first so those objects can
// be labelled before any named plane arrives.
type UnadoptedLegacyObjectsError struct {
	ControlPlaneOwner string
	Objects           []string
}

func (e *UnadoptedLegacyObjectsError) Error() string {
	return fmt.Sprintf(
		"cannot install named control plane %q while legacy NVCF objects are missing %s; upgrade the legacy control plane first so ownership labels are applied. Unadopted objects: %s",
		e.ControlPlaneOwner,
		mixedModeOwnerLabel,
		strings.Join(e.Objects, ", "),
	)
}

// CheckNamedControlPlaneInstallAllowed fails a named install when the current
// cluster still contains unlabeled objects that the legacy default plane would
// have created.
func CheckNamedControlPlaneInstallAllowed(ctx context.Context, client kubernetes.Interface, controlPlaneOwner string) error {
	if controlPlaneOwner == "" || controlPlaneOwner == mixedModeDefaultOwner {
		return nil
	}
	objects, err := FindUnadoptedLegacyObjects(ctx, client)
	if err != nil {
		return err
	}
	if len(objects) == 0 {
		return nil
	}
	return &UnadoptedLegacyObjectsError{
		ControlPlaneOwner: controlPlaneOwner,
		Objects:           objects,
	}
}

// FindUnadoptedLegacyObjects lists the bounded set of legacy fixed-name
// resources used as the mixed-mode install gate. Missing objects are ignored;
// present objects with any owner label are considered adopted for this check.
func FindUnadoptedLegacyObjects(ctx context.Context, client kubernetes.Interface) ([]string, error) {
	var out []string
	for _, namespace := range legacyControlPlaneNamespaces {
		ref := objectRef{Kind: "namespace", Name: namespace}
		if err := appendNamespaceIfUnadopted(ctx, client, ref, &out); err != nil {
			return nil, err
		}
	}
	for _, ref := range legacyNamespacedObjects {
		if err := appendNamespacedObjectIfUnadopted(ctx, client, ref, &out); err != nil {
			return nil, err
		}
	}
	for _, ref := range legacyClusterScopedObjects {
		if err := appendClusterScopedObjectIfUnadopted(ctx, client, ref, &out); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func appendNamespaceIfUnadopted(ctx context.Context, client kubernetes.Interface, ref objectRef, out *[]string) error {
	obj, err := client.CoreV1().Namespaces().Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return handleLegacyObjectReadError(ref, err)
	}
	appendIfUnadopted(ref, obj, out)
	return nil
}

func appendNamespacedObjectIfUnadopted(ctx context.Context, client kubernetes.Interface, ref objectRef, out *[]string) error {
	switch ref.Kind {
	case "deployment":
		obj, err := client.AppsV1().Deployments(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return handleLegacyObjectReadError(ref, err)
		}
		appendIfUnadopted(ref, obj, out)
	case "statefulset":
		obj, err := client.AppsV1().StatefulSets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return handleLegacyObjectReadError(ref, err)
		}
		appendIfUnadopted(ref, obj, out)
	default:
		return fmt.Errorf("unsupported legacy namespaced object kind %q", ref.Kind)
	}
	return nil
}

func appendClusterScopedObjectIfUnadopted(ctx context.Context, client kubernetes.Interface, ref objectRef, out *[]string) error {
	switch ref.Kind {
	case "clusterrole":
		obj, err := client.RbacV1().ClusterRoles().Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return handleLegacyObjectReadError(ref, err)
		}
		appendIfUnadopted(ref, obj, out)
	case "clusterrolebinding":
		obj, err := client.RbacV1().ClusterRoleBindings().Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return handleLegacyObjectReadError(ref, err)
		}
		appendIfUnadopted(ref, obj, out)
	case "mutatingwebhookconfiguration":
		obj, err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return handleLegacyObjectReadError(ref, err)
		}
		appendIfUnadopted(ref, obj, out)
	case "validatingwebhookconfiguration":
		obj, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return handleLegacyObjectReadError(ref, err)
		}
		appendIfUnadopted(ref, obj, out)
	default:
		return fmt.Errorf("unsupported legacy cluster-scoped object kind %q", ref.Kind)
	}
	return nil
}

func appendIfUnadopted(ref objectRef, obj metav1.Object, out *[]string) {
	if obj.GetLabels()[mixedModeOwnerLabel] == "" {
		*out = append(*out, ref.String())
	}
}

func handleLegacyObjectReadError(ref objectRef, err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("read legacy %s: %w", ref.String(), err)
}
