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
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	storageCapabilityCatalogAPIVersion = "storage.nvcf.nvidia.com/v1alpha1"
	storageCapabilityCatalogKind       = "StorageCapabilityCatalog"

	// StorageCapabilityConfigMapName is the stable name of the ConfigMap that
	// contains NVCA's public CSI provider capability catalog.
	StorageCapabilityConfigMapName = "nvcf-storage-capabilities"
	// StorageCapabilityConfigMapKey is the ConfigMap data key containing the
	// serialized storage capability catalog.
	StorageCapabilityConfigMapKey = "storage-provider-capabilities.yaml"

	// ModelCacheTransitionDisabled means no durable cache for a workflow.
	ModelCacheTransitionDisabled = "disabled"
	// ModelCacheTransitionROXReadOnly populates a writer claim and publishes a
	// separate ReadOnlyMany reader claim with read-only Pod mounts.
	ModelCacheTransitionROXReadOnly = "roxReadOnly"
	// ModelCacheTransitionRWXReadOnly populates one ReadWriteMany claim and
	// serves that same claim through read-only Pod mounts.
	ModelCacheTransitionRWXReadOnly = "rwxReadOnly"
	// ModelCacheProviderNVMesh is the provider id the catalog uses for NVMesh.
	// It names a driver family; it does not gate what a driver may run.
	ModelCacheProviderNVMesh = "nvmesh"
)

type storageCapabilityCatalog struct {
	APIVersion string                       `json:"apiVersion"`
	Kind       string                       `json:"kind"`
	Drivers    map[string]storageDriverSpec `json:"drivers"`
}

// storageDriverSpec records what a qualification run established for one exact
// CSI provisioner, and nothing more. How NVCA caches on that driver is derived
// from the access modes, not declared here: a ReadWriteMany claim is shared and
// mounted read-only, a ReadWriteOnce plus ReadOnlyMany pair gives readers their
// own claim, and neither means no durable cache.
type storageDriverSpec struct {
	Provider string `json:"provider"`
	// AccessModes are the modes qualified end to end in a cache workflow, not
	// the modes the driver will accept. An empty list means nothing is
	// qualified yet and caching stays off for that driver. A pointer so that an
	// absent field is rejected rather than read as empty.
	AccessModes *[]string `json:"accessModes"`
	// ReaderMountOptions apply to reader PVs NVCA creates, which only the
	// ReadWriteOnce plus ReadOnlyMany shape does. Vendor specific options
	// belong here rather than in code: norecovery and nouuid are NVMesh XFS
	// requirements and apply to no other driver.
	ReaderMountOptions *[]string `json:"readerMountOptions"`
}

func loadStorageCapabilityCatalog(
	ctx context.Context,
	c client.Client,
	namespace string,
) (*storageCapabilityCatalog, error) {
	if namespace == "" {
		return nil, fmt.Errorf("storage capability ConfigMap namespace is empty")
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: StorageCapabilityConfigMapName}, cm); err != nil {
		return nil, fmt.Errorf("get storage capability ConfigMap %s/%s: %w",
			namespace, StorageCapabilityConfigMapName, err)
	}

	raw, ok := cm.Data[StorageCapabilityConfigMapKey]
	if !ok || raw == "" {
		return nil, fmt.Errorf("storage capability ConfigMap %s/%s has no %q data",
			namespace, StorageCapabilityConfigMapName, StorageCapabilityConfigMapKey)
	}

	catalog := &storageCapabilityCatalog{}
	if err := yaml.UnmarshalStrict([]byte(raw), catalog); err != nil {
		return nil, fmt.Errorf("parse storage capability catalog: %w", err)
	}
	if err := validateStorageCapabilityCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func validateStorageCapabilityCatalog(catalog *storageCapabilityCatalog) error {
	if catalog.APIVersion != storageCapabilityCatalogAPIVersion {
		return fmt.Errorf("unsupported storage capability apiVersion %q", catalog.APIVersion)
	}
	if catalog.Kind != storageCapabilityCatalogKind {
		return fmt.Errorf("unsupported storage capability kind %q", catalog.Kind)
	}
	if len(catalog.Drivers) == 0 {
		return fmt.Errorf("storage capability catalog has no drivers")
	}

	for provisioner, driver := range catalog.Drivers {
		if strings.TrimSpace(provisioner) == "" || strings.TrimSpace(driver.Provider) == "" {
			return fmt.Errorf("storage capability catalog has an empty provisioner or provider")
		}
		if driver.AccessModes == nil {
			return fmt.Errorf("driver %q has no accessModes", provisioner)
		}
		if driver.ReaderMountOptions == nil {
			return fmt.Errorf("driver %q has no readerMountOptions", provisioner)
		}
		readerMountOptions := make(map[string]bool, len(*driver.ReaderMountOptions))
		for i, option := range *driver.ReaderMountOptions {
			if strings.TrimSpace(option) == "" {
				return fmt.Errorf("driver %q has blank readerMountOption", provisioner)
			}
			if strings.TrimSpace(option) != option {
				return fmt.Errorf("driver %q has readerMountOption %q with surrounding whitespace", provisioner, option)
			}
			if readerMountOptions[option] {
				return fmt.Errorf("driver %q has duplicate readerMountOption %q", provisioner, option)
			}
			for _, previous := range (*driver.ReaderMountOptions)[:i] {
				if negatesMountOption(previous, option) {
					return fmt.Errorf("driver %q readerMountOptions %q and %q conflict",
						provisioner, previous, option)
				}
			}
			readerMountOptions[option] = true
		}
		accessModes := make(map[string]bool, len(*driver.AccessModes))
		for _, mode := range *driver.AccessModes {
			switch mode {
			case string(corev1.ReadWriteOnce), string(corev1.ReadOnlyMany), string(corev1.ReadWriteMany):
			default:
				return fmt.Errorf("driver %q has invalid accessMode %q", provisioner, mode)
			}
			if accessModes[mode] {
				return fmt.Errorf("driver %q has duplicate accessMode %q", provisioner, mode)
			}
			accessModes[mode] = true
		}

		// A driver qualified for the ReadWriteOnce plus ReadOnlyMany shape gets
		// reader PVs that NVCA creates, so it must say how to mount them
		// read-only. The shared claim shape creates no reader PV, so it needs
		// no options.
		// ReadOnlyMany describes readers. Without a writer mode alongside it
		// there is nothing to populate the cache.
		if accessModes[string(corev1.ReadOnlyMany)] &&
			!accessModes[string(corev1.ReadWriteOnce)] && !accessModes[string(corev1.ReadWriteMany)] {
			return fmt.Errorf(
				"driver %q qualifies ReadOnlyMany with no writer mode, "+
					"it needs ReadWriteOnce or ReadWriteMany",
				provisioner)
		}
		if accessModes[string(corev1.ReadWriteOnce)] && accessModes[string(corev1.ReadOnlyMany)] &&
			!readerMountOptions["ro"] {
			return fmt.Errorf(
				"driver %q qualifies for the ReadOnlyMany reader shape and must list readerMountOption %q",
				provisioner, "ro")
		}
	}

	return nil
}
