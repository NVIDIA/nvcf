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

package api

import (
	"strings"
)

// Prefix is the prefix of every GDS label, annotation and finalizer key. The
// controller forwards a Service's labels and annotations with this prefix to
// GDS as exported labels and annotations.
const Prefix = "gds.nvidia.com/"

// Service labels that drive exports. The Rust gds-client crate mirrors these
// values.
const (
	// LabelExport set to ExportEnabled on a Service makes the controller
	// create and own a ServiceExport of the same name.
	LabelExport = Prefix + "export"
	// ExportEnabled is the value of LabelExport that enables the export.
	ExportEnabled = "true"

	// LabelRole says what GDS derives from the export: RoleRouter,
	// RolePoolFrontend, RoleKVRelay or RolePylon.
	LabelRole = Prefix + "role"
	// LabelPool associates the export with a pool record by pool name.
	LabelPool = Prefix + "pool"
)

// Values of LabelRole.
const (
	// RoleRouter derives a Router from the export.
	RoleRouter = "router"
	// RolePoolFrontend makes the export the OpenAI frontend of the pool
	// named by LabelPool.
	RolePoolFrontend = "pool-frontend"
	// RoleKVRelay makes the export the KV event relay of the pool named by
	// LabelPool.
	RoleKVRelay = "kv-relay"
	// RolePylon makes the export the Pylon transport of the pool named by
	// LabelPool: an ingress that dials routers and carries requests over its
	// reverse tunnel.
	RolePylon = "pylon"
)

// Service annotations carrying pool and router metadata.
const (
	// AnnotationPoolModels is a comma-separated list of model ids.
	AnnotationPoolModels = Prefix + "pool-models"
	// AnnotationHardwareClass is a free-form hardware class, for example
	// b200-ib.
	AnnotationHardwareClass = Prefix + "hardware-class"
	// AnnotationDC is the data center of the pool.
	AnnotationDC = Prefix + "dc"
	// AnnotationDialScheme is the scheme clients dial the exported Service
	// with, through router seeds or the derived VIP: DialSchemeHTTP or
	// DialSchemeHTTPS.
	AnnotationDialScheme = Prefix + "dial-scheme"
	// AnnotationGRPCPortName names the Service port that carries gRPC.
	AnnotationGRPCPortName = Prefix + "grpc-port-name"
	// AnnotationHTTPPortName names the Service port that carries HTTP.
	AnnotationHTTPPortName = Prefix + "http-port-name"
	// AnnotationQUICPortName names the Service port that carries the QUIC
	// reverse tunnel (UDP).
	AnnotationQUICPortName = Prefix + "quic-port-name"
	// AnnotationRoles is a comma-separated list of pool roles for a process
	// that fills several (a frontend embedding Pylon). When present and
	// non-empty it replaces LabelRole for pool derivation. RoleRouter is not
	// allowed in it.
	AnnotationRoles = Prefix + "roles"
)

// Values and defaults of the annotations above.
const (
	DialSchemeHTTP  = "http"
	DialSchemeHTTPS = "https"

	// DefaultGRPCPortName is used when AnnotationGRPCPortName is absent.
	DefaultGRPCPortName = "grpc"
	// DefaultHTTPPortName is used when AnnotationHTTPPortName is absent.
	DefaultHTTPPortName = "http"
	// DefaultQUICPortName is used when AnnotationQUICPortName is absent.
	DefaultQUICPortName = "quic"
)

// Labels and finalizers on objects the gds-mcs-controller creates.
const (
	// LabelManagedBy marks objects the controller created; the value is
	// ManagedByController.
	LabelManagedBy = Prefix + "managed-by"
	// ManagedByController is the value of LabelManagedBy.
	ManagedByController = "gds-mcs-controller"
	// LabelClusterSet scopes controller-created objects to a clusterset.
	LabelClusterSet = Prefix + "clusterset"
	// LabelPoolID marks a ServiceExport created for an API-registered pool.
	LabelPoolID = Prefix + "pool-id"
	// FinalizerServiceExport lets the controller deregister an export from
	// GDS before the ServiceExport is deleted.
	FinalizerServiceExport = Prefix + "service-export"
	// FieldManager is the server-side apply field manager of the controller.
	FieldManager = "gds-mcs-controller"
	// LeaderElectionID is the name of the controller's leader election Lease.
	LeaderElectionID = "gds-mcs-controller.gds.nvidia.com"
)

// Kubernetes and KEP-1645 keys the controller writes.
const (
	// LabelEndpointSliceManagedBy is the EndpointSlice managed-by label;
	// imported slices carry EndpointSliceManagedBy.
	LabelEndpointSliceManagedBy = "endpointslice.kubernetes.io/managed-by"
	// EndpointSliceManagedBy is the value of LabelEndpointSliceManagedBy on
	// imported slices.
	EndpointSliceManagedBy = "gds-mcs-controller.gds.nvidia.com"
	// LabelKubernetesServiceName ties an EndpointSlice to its Service (the
	// derived Service for imported slices).
	LabelKubernetesServiceName = "kubernetes.io/service-name"
	// LabelMCSServiceName names the multi-cluster service of an
	// EndpointSlice.
	LabelMCSServiceName = "multicluster.kubernetes.io/service-name"
	// LabelMCSSourceCluster names the cluster an imported EndpointSlice
	// comes from.
	LabelMCSSourceCluster = "multicluster.kubernetes.io/source-cluster"
	// AnnotationMCSDerivedService on a ServiceImport names its derived
	// Service.
	AnnotationMCSDerivedService = "multicluster.kubernetes.io/derived-service"
	// ClusterPropertyClusterSet is the ClusterProperty holding the clusterset
	// name.
	ClusterPropertyClusterSet = "clusterset.k8s.io"
	// ClusterPropertyClusterID is the ClusterProperty holding the cluster id.
	ClusterPropertyClusterID = "cluster.clusterset.k8s.io"
)

// IsExported reports whether Service labels request a label-driven export.
func IsExported(labels map[string]string) bool {
	return labels[LabelExport] == ExportEnabled
}

// IsRole reports whether role is one of the values of LabelRole.
func IsRole(role string) bool {
	switch role {
	case RoleRouter, RolePoolFrontend, RoleKVRelay, RolePylon:
		return true
	}
	return false
}

// IsPoolRole reports whether role attaches an export to a pool.
func IsPoolRole(role string) bool {
	switch role {
	case RolePoolFrontend, RoleKVRelay, RolePylon:
		return true
	}
	return false
}

// ParseRoles returns the pool roles of an exported Service: the entries of
// AnnotationRoles when it is present and non-empty, else the LabelRole value.
// Unknown entries, RoleRouter and repeats are dropped; order is first seen.
// The result is empty for a router export and for a Service without roles.
func ParseRoles(labels, annotations map[string]string) []string {
	var roles []string
	seen := map[string]bool{}
	add := func(r string) {
		r = strings.TrimSpace(r)
		if !IsPoolRole(r) || seen[r] {
			return
		}
		seen[r] = true
		roles = append(roles, r)
	}
	if v := strings.TrimSpace(annotations[AnnotationRoles]); v != "" {
		for _, r := range strings.Split(v, ",") {
			add(r)
		}
		return roles
	}
	add(labels[LabelRole])
	return roles
}

// IsGDSKey reports whether a label or annotation key has the GDS prefix.
func IsGDSKey(key string) bool {
	return strings.HasPrefix(key, Prefix) && len(key) > len(Prefix)
}

// ParsePoolModels splits an AnnotationPoolModels value into model ids. It
// trims spaces and drops empty and repeated entries, keeping first-seen order.
func ParsePoolModels(value string) []string {
	var models []string
	seen := map[string]bool{}
	for _, m := range strings.Split(value, ",") {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		models = append(models, m)
	}
	return models
}

// PortNames returns the gRPC and HTTP port names from Service annotations,
// falling back to DefaultGRPCPortName and DefaultHTTPPortName.
func PortNames(annotations map[string]string) (grpcPort, httpPort string) {
	grpcPort, httpPort = DefaultGRPCPortName, DefaultHTTPPortName
	if v := strings.TrimSpace(annotations[AnnotationGRPCPortName]); v != "" {
		grpcPort = v
	}
	if v := strings.TrimSpace(annotations[AnnotationHTTPPortName]); v != "" {
		httpPort = v
	}
	return grpcPort, httpPort
}

// QUICPortName returns the QUIC port name from Service annotations, falling
// back to DefaultQUICPortName.
func QUICPortName(annotations map[string]string) string {
	if v := strings.TrimSpace(annotations[AnnotationQUICPortName]); v != "" {
		return v
	}
	return DefaultQUICPortName
}
