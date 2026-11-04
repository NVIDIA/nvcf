/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SchemeGroupVersion for the NvSnap CRDs.
//
// Group "nvsnap.nvcf.nvidia.io" follows the NVCA convention
// (matching nvca.nvcf.nvidia.io etc.) — the CRDs are NVCA-managed
// state, not part of the NvSnap cluster service's API. The design doc
// originally proposed "nvsnap.nvidia.com" but consistency with sibling
// groups in this repo wins for clarity ("everything under
// .nvcf.nvidia.io is NVCA-related").
var SchemeGroupVersion = schema.GroupVersion{
	Group:   "nvsnap.nvcf.nvidia.io",
	Version: "v1alpha1",
}

var (
	SchemeBuilder      runtime.SchemeBuilder
	localSchemeBuilder = &SchemeBuilder
	AddToScheme        = localSchemeBuilder.AddToScheme
)

func init() {
	// Manual registrations only; generated registrations live in
	// zz_generated.deepcopy.go-adjacent files.
	localSchemeBuilder.Register(addKnownTypes)
}

// Resource takes an unqualified resource and returns a Group qualified GroupResource.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(
		SchemeGroupVersion,
		&NvSnapFunctionState{},
		&NvSnapFunctionStateList{},
	)
	scheme.AddKnownTypes(
		SchemeGroupVersion,
		&metav1.Status{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
