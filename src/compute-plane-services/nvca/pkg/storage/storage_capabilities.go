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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
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

	// ModelCacheTransitionDisabled prevents durable storage for a workflow.
	ModelCacheTransitionDisabled = "disabled"
	// ModelCacheTransitionROXReadOnly populates a writer claim and publishes a
	// separate ReadOnlyMany reader claim with read-only Pod mounts.
	ModelCacheTransitionROXReadOnly = "roxReadOnly"
	// ModelCacheTransitionRWXReadOnly selects a regular model-cache transition
	// that populates one ReadWriteMany claim and serves that same claim through
	// read-only Pod mounts.
	ModelCacheTransitionRWXReadOnly = "rwxReadOnly"
	// ModelCacheProviderNVMesh is the provider id the catalog uses for NVMesh.
	// It identifies a driver family for tests and diagnostics; it does not
	// gate which transitions a driver may run.
	ModelCacheProviderNVMesh = "nvmesh"
)

var (
	// ErrModelCacheStorageClassNotFound means nvcf-sc is absent. Callers may map
	// this to a documented non-durable fallback.
	ErrModelCacheStorageClassNotFound = errors.New("model cache StorageClass not found")
	// ErrModelCacheStorageSelectionDrift marks a deterministic mismatch between
	// a persisted selection and its live StorageClass or catalog input. Callers
	// can fail the request without treating transient API errors as drift.
	ErrModelCacheStorageSelectionDrift = errors.New("model cache storage selection drift")
)

// ModelCacheWorkflow selects one transition column from the catalog.
type ModelCacheWorkflow string

const (
	ModelCacheWorkflowRegular ModelCacheWorkflow = "regularModelCache"
	ModelCacheWorkflowHelm    ModelCacheWorkflow = "helmModelCache"
)

// ModelCacheStorageSelection is the durable-storage decision derived from the
// live nvcf-sc object and the public capability catalog. It deliberately does
// not infer behavior from a provider name or access mode.
type ModelCacheStorageSelection struct {
	StorageClassName     string
	StorageClassUID      types.UID
	StorageClassDigest   string
	ProfileDigest        string
	CatalogRevision      string
	Provider             string
	Provisioner          string
	Transition           string
	RequiredAccessModes  []corev1.PersistentVolumeAccessMode
	RequiredMountOptions []string
}

type storageCapabilityCatalog struct {
	APIVersion string                       `json:"apiVersion"`
	Kind       string                       `json:"kind"`
	Drivers    map[string]storageDriverSpec `json:"drivers"`
}

type storageDriverSpec struct {
	Provider           string              `json:"provider"`
	AccessModes        *[]string           `json:"accessModes"`
	ReaderMountOptions *[]string           `json:"readerMountOptions"`
	Capabilities       storageCapabilities `json:"capabilities,omitempty"`
	Transitions        storageTransitions  `json:"transitions"`
}

// storageCapabilities records driver behaviors that a transition needs but
// that no PVC access mode expresses.
type storageCapabilities struct {
	// CrossNamespaceVolumeSharing means one populated volume can be exposed
	// read-only in another namespace by deriving a second PV from the first.
	// NVCA derives the reader by rewriting the namespace segment of the CSI
	// volume handle, so only a driver whose handles encode the namespace that
	// way can do it. Helm model caching is cross-namespace by nature, so
	// roxReadOnly is only available to the Helm workflow on such a driver.
	// Every other backend must serve Helm caching from one ReadWriteMany
	// claim instead.
	CrossNamespaceVolumeSharing bool `json:"crossNamespaceVolumeSharing,omitempty"`
}

type storageTransitions struct {
	RegularModelCache string `json:"regularModelCache"`
	HelmModelCache    string `json:"helmModelCache"`
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

	return parseStorageCapabilityCatalog(raw)
}

func loadStorageCapabilityCatalogSnapshot(
	ctx context.Context,
	c client.Client,
	namespace string,
) (*storageCapabilityCatalog, string, error) {
	if namespace == "" {
		return nil, "", fmt.Errorf("storage capability ConfigMap namespace is empty")
	}

	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: StorageCapabilityConfigMapName}, cm); err != nil {
		return nil, "", fmt.Errorf("get storage capability ConfigMap %s/%s: %w",
			namespace, StorageCapabilityConfigMapName, err)
	}
	raw, ok := cm.Data[StorageCapabilityConfigMapKey]
	if !ok || raw == "" {
		return nil, "", fmt.Errorf("storage capability ConfigMap %s/%s has no %q data",
			namespace, StorageCapabilityConfigMapName, StorageCapabilityConfigMapKey)
	}
	catalog, err := parseStorageCapabilityCatalog(raw)
	if err != nil {
		return nil, "", err
	}
	return catalog, digestCatalogPayload(raw), nil
}

func parseStorageCapabilityCatalog(raw string) (*storageCapabilityCatalog, error) {
	catalog := &storageCapabilityCatalog{}
	if err := yaml.UnmarshalStrict([]byte(raw), catalog); err != nil {
		return nil, fmt.Errorf("parse storage capability catalog: %w", err)
	}
	if err := validateStorageCapabilityCatalog(catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

// ResolveModelCacheStorage resolves the exact transition for a workflow. A
// missing nvcf-sc is returned as a sentinel so callers that support an
// ephemeral cache can choose that fallback. Invalid or unsafe configuration is
// an error and must not silently select another durable provider.
func ResolveModelCacheStorage(
	ctx context.Context,
	c client.Client,
	catalogNamespace string,
	workflow ModelCacheWorkflow,
) (*ModelCacheStorageSelection, error) {
	sc := &storagev1.StorageClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: DefaultModelCacheStorageClassName}, sc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrModelCacheStorageClassNotFound
		}
		return nil, fmt.Errorf("get model cache StorageClass %q: %w", DefaultModelCacheStorageClassName, err)
	}

	catalog, catalogDigest, err := loadStorageCapabilityCatalogSnapshot(ctx, c, catalogNamespace)
	if err != nil {
		return nil, err
	}
	return selectModelCacheStorageFromObjects(sc, catalog, catalogDigest, workflow)
}

// ResolveModelCacheStorageWithClientset provides the same decision to the
// regular container model-cache path, which uses client-go rather than a
// controller-runtime client.
func ResolveModelCacheStorageWithClientset(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	catalogNamespace string,
	workflow ModelCacheWorkflow,
) (*ModelCacheStorageSelection, error) {
	if catalogNamespace == "" {
		return nil, fmt.Errorf("storage capability ConfigMap namespace is empty")
	}

	sc, err := k8sClient.StorageV1().StorageClasses().Get(
		ctx, DefaultModelCacheStorageClassName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrModelCacheStorageClassNotFound
		}
		return nil, fmt.Errorf("get model cache StorageClass %q: %w", DefaultModelCacheStorageClassName, err)
	}
	cm, err := k8sClient.CoreV1().ConfigMaps(catalogNamespace).Get(
		ctx, StorageCapabilityConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get storage capability ConfigMap %s/%s: %w",
			catalogNamespace, StorageCapabilityConfigMapName, err)
	}
	raw, ok := cm.Data[StorageCapabilityConfigMapKey]
	if !ok || raw == "" {
		return nil, fmt.Errorf("storage capability ConfigMap %s/%s has no %q data",
			catalogNamespace, StorageCapabilityConfigMapName, StorageCapabilityConfigMapKey)
	}
	catalog, err := parseStorageCapabilityCatalog(raw)
	if err != nil {
		return nil, err
	}
	return selectModelCacheStorageFromObjects(sc, catalog, digestCatalogPayload(raw), workflow)
}

func selectModelCacheStorageFromObjects(
	sc *storagev1.StorageClass,
	catalog *storageCapabilityCatalog,
	catalogDigest string,
	workflow ModelCacheWorkflow,
) (*ModelCacheStorageSelection, error) {
	if sc.ReclaimPolicy == nil || *sc.ReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return nil, fmt.Errorf("model cache StorageClass %q must use reclaimPolicy Retain",
			DefaultModelCacheStorageClassName)
	}
	if strings.TrimSpace(sc.Provisioner) == "" {
		return nil, fmt.Errorf("model cache StorageClass %q has an empty provisioner",
			DefaultModelCacheStorageClassName)
	}

	driver, ok := catalog.Drivers[sc.Provisioner]
	if !ok {
		return nil, fmt.Errorf("model cache StorageClass %q uses provisioner %q with no catalog entry",
			DefaultModelCacheStorageClassName, sc.Provisioner)
	}

	var transition string
	switch workflow {
	case ModelCacheWorkflowRegular:
		transition = driver.Transitions.RegularModelCache
	case ModelCacheWorkflowHelm:
		transition = driver.Transitions.HelmModelCache
	default:
		return nil, fmt.Errorf("unknown model cache workflow %q", workflow)
	}
	var requiredMountOptions []string
	if transition == ModelCacheTransitionROXReadOnly {
		requiredMountOptions = append([]string(nil), (*driver.ReaderMountOptions)...)
	}

	return &ModelCacheStorageSelection{
		StorageClassName:     sc.Name,
		StorageClassUID:      sc.UID,
		StorageClassDigest:   digestStorageClass(sc),
		ProfileDigest:        digestDriverProfile(sc.Provisioner, driver, workflow, transition),
		CatalogRevision:      catalogDigest,
		Provider:             driver.Provider,
		Provisioner:          sc.Provisioner,
		Transition:           transition,
		RequiredAccessModes:  requiredAccessModesForTransition(transition),
		RequiredMountOptions: requiredMountOptions,
	}, nil
}

func requiredAccessModesForTransition(transition string) []corev1.PersistentVolumeAccessMode {
	switch transition {
	case ModelCacheTransitionROXReadOnly:
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce, corev1.ReadOnlyMany}
	case ModelCacheTransitionRWXReadOnly:
		return []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}
	default:
		return nil
	}
}

type canonicalStorageClassParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type canonicalStorageClass struct {
	Provisioner       string                           `json:"provisioner"`
	Parameters        []canonicalStorageClassParameter `json:"parameters"`
	ReclaimPolicy     string                           `json:"reclaimPolicy"`
	VolumeBindingMode string                           `json:"volumeBindingMode"`
	MountOptions      []string                         `json:"mountOptions"`
	AllowedTopologies []corev1.TopologySelectorTerm    `json:"allowedTopologies"`
}

func digestStorageClass(sc *storagev1.StorageClass) string {
	parameterNames := make([]string, 0, len(sc.Parameters))
	for name := range sc.Parameters {
		parameterNames = append(parameterNames, name)
	}
	sort.Strings(parameterNames)
	parameters := make([]canonicalStorageClassParameter, 0, len(parameterNames))
	for _, name := range parameterNames {
		parameters = append(parameters, canonicalStorageClassParameter{Name: name, Value: sc.Parameters[name]})
	}

	reclaimPolicy := ""
	if sc.ReclaimPolicy != nil {
		reclaimPolicy = string(*sc.ReclaimPolicy)
	}
	volumeBindingMode := ""
	if sc.VolumeBindingMode != nil {
		volumeBindingMode = string(*sc.VolumeBindingMode)
	}
	canonical := canonicalStorageClass{
		Provisioner:       sc.Provisioner,
		Parameters:        parameters,
		ReclaimPolicy:     reclaimPolicy,
		VolumeBindingMode: volumeBindingMode,
		MountOptions:      append([]string{}, sc.MountOptions...),
		AllowedTopologies: append([]corev1.TopologySelectorTerm{}, sc.AllowedTopologies...),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		panic(fmt.Sprintf("marshal canonical StorageClass: %v", err))
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("v1:sha256:%x", sum)
}

// canonicalDriverProfile is the stable, marshalled form of the one catalog
// entry a decision depends on. Field order is fixed by the struct, so the
// digest is independent of how the catalog YAML happens to be written.
type canonicalDriverProfile struct {
	Provisioner        string   `json:"provisioner"`
	Provider           string   `json:"provider"`
	Workflow           string   `json:"workflow"`
	Transition         string   `json:"transition"`
	AccessModes        []string `json:"accessModes"`
	ReaderMountOptions []string `json:"readerMountOptions,omitempty"`
}

// digestDriverProfile hashes the qualified profile behind a decision. Two
// catalogs that describe this driver and workflow identically produce the same
// digest even if they differ elsewhere, which is what lets an existing binding
// be reused across an unrelated catalog edit.
func digestDriverProfile(
	provisioner string,
	driver storageDriverSpec,
	workflow ModelCacheWorkflow,
	transition string,
) string {
	profile := canonicalDriverProfile{
		Provisioner: provisioner,
		Provider:    driver.Provider,
		Workflow:    string(workflow),
		Transition:  transition,
	}
	if driver.AccessModes != nil {
		profile.AccessModes = append([]string(nil), (*driver.AccessModes)...)
		sort.Strings(profile.AccessModes)
	}
	if driver.ReaderMountOptions != nil {
		// Order is behavior for mount options, so it is preserved.
		profile.ReaderMountOptions = append([]string(nil), (*driver.ReaderMountOptions)...)
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		// canonicalDriverProfile contains only strings and string slices.
		panic(fmt.Sprintf("marshal canonical driver profile: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum)
}

func digestCatalogPayload(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("sha256:%x", sum)
}

// ValidateModelCacheStorageSelectionLive verifies that a persisted durable
// decision still points at the same immutable StorageClass before its first
// storage side effect. It does not reselect from the current catalog.
func ValidateModelCacheStorageSelectionLive(
	ctx context.Context,
	c client.Client,
	selection *PersistedModelCacheStorageSelection,
) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	if selection.Mode != ModelCacheSelectionDurable {
		return nil
	}

	sc := &storagev1.StorageClass{}
	if err := c.Get(ctx, client.ObjectKey{Name: selection.StorageClassName}, sc); err != nil {
		return fmt.Errorf("get selected model cache StorageClass %q: %w", selection.StorageClassName, err)
	}
	return validateSelectedStorageClass(sc, selection)
}

// ValidateModelCacheStorageSelectionLiveWithClientset is the client-go
// equivalent used by the regular container model-cache path.
func ValidateModelCacheStorageSelectionLiveWithClientset(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	selection *PersistedModelCacheStorageSelection,
) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	if selection.Mode != ModelCacheSelectionDurable {
		return nil
	}
	sc, err := k8sClient.StorageV1().StorageClasses().Get(
		ctx, selection.StorageClassName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get selected model cache StorageClass %q: %w", selection.StorageClassName, err)
	}
	return validateSelectedStorageClass(sc, selection)
}

// ValidateModelCacheStorageSelectionInputsWithClientset verifies every live
// input captured by a durable selection immediately before its first binding
// is created. Once the binding exists, callers use the immutable binding and
// must not reselect from a later catalog revision.
func ValidateModelCacheStorageSelectionInputsWithClientset(
	ctx context.Context,
	k8sClient kubernetes.Interface,
	catalogNamespace string,
	selection *PersistedModelCacheStorageSelection,
) error {
	if err := ValidateModelCacheStorageSelectionLiveWithClientset(ctx, k8sClient, selection); err != nil {
		return err
	}
	if selection.Mode != ModelCacheSelectionDurable {
		return nil
	}
	if strings.TrimSpace(catalogNamespace) == "" {
		return fmt.Errorf("%w: storage capability ConfigMap namespace is empty",
			ErrModelCacheStorageSelectionDrift)
	}

	cm, err := k8sClient.CoreV1().ConfigMaps(catalogNamespace).Get(
		ctx, StorageCapabilityConfigMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get storage capability ConfigMap %s/%s before binding creation: %w",
			catalogNamespace, StorageCapabilityConfigMapName, err)
	}
	raw, ok := cm.Data[StorageCapabilityConfigMapKey]
	if !ok || raw == "" {
		return fmt.Errorf("%w: storage capability ConfigMap %s/%s has no %q data",
			ErrModelCacheStorageSelectionDrift,
			catalogNamespace, StorageCapabilityConfigMapName, StorageCapabilityConfigMapKey)
	}
	catalog, err := parseStorageCapabilityCatalog(raw)
	if err != nil {
		return fmt.Errorf("%w: selected storage capability catalog is invalid: %v",
			ErrModelCacheStorageSelectionDrift, err)
	}
	driver, ok := catalog.Drivers[selection.Provisioner]
	if !ok {
		return fmt.Errorf("%w: selected provisioner %q has no catalog entry",
			ErrModelCacheStorageSelectionDrift, selection.Provisioner)
	}
	if driver.Provider != selection.Provider {
		return fmt.Errorf("%w: selected provisioner %q provider changed from %q to %q",
			ErrModelCacheStorageSelectionDrift,
			selection.Provisioner, selection.Provider, driver.Provider)
	}
	var transition string
	switch selection.Workflow {
	case ModelCacheWorkflowRegular:
		transition = driver.Transitions.RegularModelCache
	case ModelCacheWorkflowHelm:
		transition = driver.Transitions.HelmModelCache
	default:
		return fmt.Errorf("%w: unknown model cache workflow %q",
			ErrModelCacheStorageSelectionDrift, selection.Workflow)
	}
	if transition != selection.Transition {
		return fmt.Errorf("%w: selected provisioner %q transition for %s changed from %q to %q",
			ErrModelCacheStorageSelectionDrift,
			selection.Provisioner, selection.Workflow, selection.Transition, transition)
	}
	// Compare the qualified profile rather than the whole catalog payload, so
	// an unrelated catalog edit does not invalidate this selection while a
	// change to the driver's own qualified capabilities still does.
	if digest := digestDriverProfile(
		selection.Provisioner, driver, selection.Workflow, transition,
	); digest != selection.ProfileDigest {
		return fmt.Errorf("%w: selected provisioner %q qualified profile digest changed from %q to %q",
			ErrModelCacheStorageSelectionDrift, selection.Provisioner, selection.ProfileDigest, digest)
	}
	return nil
}

func validateSelectedStorageClass(
	sc *storagev1.StorageClass,
	selection *PersistedModelCacheStorageSelection,
) error {
	if sc.UID != selection.StorageClassUID {
		return fmt.Errorf("%w: selected model cache StorageClass %q UID changed from %q to %q",
			ErrModelCacheStorageSelectionDrift,
			selection.StorageClassName, selection.StorageClassUID, sc.UID)
	}
	if sc.Provisioner != selection.Provisioner {
		return fmt.Errorf("%w: selected model cache StorageClass %q provisioner changed from %q to %q",
			ErrModelCacheStorageSelectionDrift,
			selection.StorageClassName, selection.Provisioner, sc.Provisioner)
	}
	if sc.ReclaimPolicy == nil || *sc.ReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		return fmt.Errorf("%w: selected model cache StorageClass %q no longer uses reclaimPolicy Retain",
			ErrModelCacheStorageSelectionDrift,
			selection.StorageClassName)
	}
	if digest := digestStorageClass(sc); digest != selection.StorageClassDigest {
		return fmt.Errorf("%w: selected model cache StorageClass %q configuration digest changed from %q to %q",
			ErrModelCacheStorageSelectionDrift,
			selection.StorageClassName, selection.StorageClassDigest, digest)
	}
	return nil
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

		for workflow, strategy := range map[string]string{
			"regularModelCache": driver.Transitions.RegularModelCache,
			"helmModelCache":    driver.Transitions.HelmModelCache,
		} {
			if strategy != ModelCacheTransitionDisabled && strategy != ModelCacheTransitionROXReadOnly &&
				strategy != ModelCacheTransitionRWXReadOnly {
				return fmt.Errorf("driver %q transition %s has invalid strategy %q", provisioner, workflow, strategy)
			}
			switch strategy {
			case ModelCacheTransitionDisabled:
				continue
			case ModelCacheTransitionROXReadOnly:
				// roxReadOnly describes a volume shape, not a vendor: one
				// writer claim plus many read-only reader claims. Any driver
				// qualified for both modes can run it, and the reader options
				// a given driver needs are its own catalog data. Pinning this
				// to one provisioner, or requiring another driver's
				// filesystem flags, would make the catalog unable to enable a
				// provider without a code change.
				if !accessModes[string(corev1.ReadWriteOnce)] || !accessModes[string(corev1.ReadOnlyMany)] {
					return fmt.Errorf("driver %q transition %s strategy %s requires ReadWriteOnce and ReadOnlyMany access modes",
						provisioner, workflow, strategy)
				}
				// "ro" is required by the transition itself: the reader PV is
				// mounted read-only whatever the backend.
				if !readerMountOptions["ro"] {
					return fmt.Errorf("driver %q transition %s strategy %s requires readerMountOption %q",
						provisioner, workflow, strategy, "ro")
				}
				// Regular caching keeps its reader in the same namespace, so
				// proven access modes are enough. Helm caching must reach
				// another namespace, which needs the derived-reader capability.
				if workflow == string(ModelCacheWorkflowHelm) && !driver.Capabilities.CrossNamespaceVolumeSharing {
					return fmt.Errorf(
						"driver %q transition %s strategy %s requires capability crossNamespaceVolumeSharing; "+
							"a driver without it must serve Helm model caching from a ReadWriteMany claim",
						provisioner, workflow, strategy)
				}
			case ModelCacheTransitionRWXReadOnly:
				if workflow != string(ModelCacheWorkflowRegular) {
					return fmt.Errorf("driver %q transition %s strategy %s is only supported for %s",
						provisioner, workflow, strategy, ModelCacheWorkflowRegular)
				}
				if !accessModes[string(corev1.ReadWriteMany)] {
					return fmt.Errorf("driver %q transition %s strategy %s requires ReadWriteMany access mode",
						provisioner, workflow, strategy)
				}
				if len(*driver.ReaderMountOptions) != 0 {
					return fmt.Errorf("driver %q transition %s strategy %s does not create a reader PV and requires empty readerMountOptions",
						provisioner, workflow, strategy)
				}
			}
		}
	}

	return nil
}
