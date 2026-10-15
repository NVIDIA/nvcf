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

package clusterdump

import "k8s.io/apimachinery/pkg/runtime/schema"

// coreStackGVRs are the namespaced built-in resources captured for every probed
// namespace in deep mode. They are listed via the dynamic client so each object
// serialises with apiVersion/kind intact. A kind that is absent or not
// permitted on a given cluster degrades to a skip, not an error.
var coreStackGVRs = []schema.GroupVersionResource{
	{Group: "", Version: "v1", Resource: "pods"},
	{Group: "", Version: "v1", Resource: "services"},
	{Group: "", Version: "v1", Resource: "endpoints"},
	{Group: "", Version: "v1", Resource: "configmaps"},
	{Group: "", Version: "v1", Resource: "secrets"},
	{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
	{Group: "", Version: "v1", Resource: "serviceaccounts"},
	{Group: "apps", Version: "v1", Resource: "deployments"},
	{Group: "apps", Version: "v1", Resource: "statefulsets"},
	{Group: "apps", Version: "v1", Resource: "daemonsets"},
	{Group: "apps", Version: "v1", Resource: "replicasets"},
	{Group: "batch", Version: "v1", Resource: "jobs"},
	{Group: "batch", Version: "v1", Resource: "cronjobs"},
}

// nvcaStackGVRs are the NVCA / function-deployment CRDs captured on the compute
// plane in addition to coreStackGVRs. The nvca.nvcf.nvidia.io group/versions
// come from the nvca-operator CRD manifests and are authoritative. The DRA
// (resource.k8s.io / resource.nvidia.com) and Flux GVRs are best-effort and
// version-resilient: a wrong or absent version degrades to a NotFound skip.
var nvcaStackGVRs = []schema.GroupVersionResource{
	{Group: "nvca.nvcf.nvidia.io", Version: "v2beta1", Resource: "storagerequests"},
	{Group: "nvca.nvcf.nvidia.io", Version: "v1alpha1", Resource: "miniservices"},
	{Group: "resource.nvidia.com", Version: "v1beta1", Resource: "computedomains"},
	{Group: "resource.k8s.io", Version: "v1alpha3", Resource: "resourceclaims"},
	{Group: "resource.k8s.io", Version: "v1alpha3", Resource: "resourceclaimtemplates"},
	{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
	{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"},
}

// icmsRequestGVR is the ICMSRequest CR (the function-deployment request the NVCA
// operator reconciles). Captured separately so failed requests can be summarised.
var icmsRequestGVR = schema.GroupVersionResource{
	Group: "nvca.nvcf.nvidia.io", Version: "v2beta1", Resource: "icmsrequests",
}

// icmsBackendNamespace is where ICMSRequests and container-function workloads
// live; helm functions instead live in a namespace named after the request.
const icmsBackendNamespace = "nvcf-backend"

// stackListPageSize bounds each List call so a busy namespace does not force the
// API server to return everything in one response.
const stackListPageSize int64 = 500
