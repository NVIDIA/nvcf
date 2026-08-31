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

package operator

import (
	"context"
	"strings"
	"testing"

	nvcaenvtest "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/envtest"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
	nvcaclientset "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/client/clientset/versioned"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_setupCRDs(t *testing.T) {
	b := &BackendK8sCache{
		clients: mockKubeClients(),
	}
	ctx := context.Background()

	err := b.setupCRDs(ctx)
	require.NoError(t, err)

	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ModelCacheBindingCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "storagerequests.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "miniservices.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)

	// Try again, should get no error.
	err = b.setupCRDs(ctx)
	require.NoError(t, err)

	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ICMSRequestCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, ModelCacheBindingCRDName, metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "storagerequests.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)
	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, "miniservices.nvca.nvcf.nvidia.io", metav1.GetOptions{})
	require.NoError(t, err)
}

func TestModelCacheBindingCRDContract(t *testing.T) {
	crd, err := decodeCRD(modelCacheBindingsCRDData)
	require.NoError(t, err)
	assert.Equal(t, ModelCacheBindingCRDName, crd.Name)
	assert.Equal(t, apiextv1.NamespaceScoped, crd.Spec.Scope)
	require.Len(t, crd.Spec.Versions, 1)

	version := crd.Spec.Versions[0]
	assert.Equal(t, "v2beta1", version.Name)
	assert.True(t, version.Served)
	assert.True(t, version.Storage)
	require.NotNil(t, version.Subresources)
	require.NotNil(t, version.Subresources.Status)
	require.NotNil(t, version.Schema)
	require.NotNil(t, version.Schema.OpenAPIV3Schema)

	root := version.Schema.OpenAPIV3Schema
	specSchema := root.Properties["spec"]
	require.Len(t, specSchema.XValidations, 1)
	assert.Equal(t, "self == oldSelf", specSchema.XValidations[0].Rule)

	decisionSchema := specSchema.Properties["decision"]
	assert.Equal(t, "^sha256:[a-f0-9]{64}$", decisionSchema.Properties["profileDigest"].Pattern)
	assert.Equal(t, "^sha256:[a-f0-9]{64}$", decisionSchema.Properties["catalogRevision"].Pattern)
	assert.Contains(t, decisionSchema.Required, "profileDigest")
	assert.NotContains(t, decisionSchema.Required, "catalogRevision",
		"the catalog revision is audit metadata, not part of the binding identity")
	assert.Contains(t, decisionSchema.Required, "encryptionRequired")
	requiredAccessModesSchema := decisionSchema.Properties["requiredAccessModes"]
	require.NotNil(t, requiredAccessModesSchema.XListType)
	assert.Equal(t, "set", *requiredAccessModesSchema.XListType)

	storageClassSchema := specSchema.Properties["storageClass"]
	assert.Equal(t, "^v1:sha256:[a-f0-9]{64}$",
		storageClassSchema.Properties["configurationDigest"].Pattern)
	reclaimPolicySchema := storageClassSchema.Properties["reclaimPolicy"]
	require.Len(t, reclaimPolicySchema.Enum, 1)
	assert.JSONEq(t, `"Retain"`, string(reclaimPolicySchema.Enum[0].Raw))

	resourceSchema := specSchema.Properties["resources"]
	assert.Contains(t, resourceSchema.Required, "writerNamespace")
	assert.NotContains(t, resourceSchema.Required, "leaseName")
	assert.Contains(t, resourceSchema.Properties, "storageClassNames")
	assert.Contains(t, resourceSchema.Properties, "secretNames")
	for _, field := range []string{"persistentVolumeClaimNames", "persistentVolumeNames", "jobNames", "storageClassNames", "secretNames"} {
		schema := resourceSchema.Properties[field]
		require.NotNil(t, schema.XListType, field)
		assert.Equal(t, "set", *schema.XListType, field)
	}

	statusSchema := root.Properties["status"]
	require.Len(t, statusSchema.XValidations, 2)
	assert.Contains(t, statusSchema.XValidations[0].Rule, "Retiring")
	assert.Contains(t, statusSchema.XValidations[1].Rule, "providerDataIdentity")
	requestReferencesSchema := statusSchema.Properties["requestReferences"]
	require.NotNil(t, requestReferencesSchema.XListType)
	assert.Equal(t, "map", *requestReferencesSchema.XListType)
}

func TestModelCacheBindingCRDEnforcement(t *testing.T) {
	cfg, k8sClient, cleanup, err := nvcaenvtest.SetupEnvtest()
	require.NoError(t, err)
	t.Cleanup(cleanup)

	ctx := context.Background()
	const namespace = "model-cache-binding-test"
	_, err = k8sClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	nvcaClient, err := nvcaclientset.NewForConfig(cfg)
	require.NoError(t, err)
	bindings := nvcaClient.NvcaV2beta1().ModelCacheBindings(namespace)

	catalogDigest := "sha256:" + strings.Repeat("0", 64)
	binding := &nvcav2beta1.ModelCacheBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "regular-cache", Namespace: namespace},
		Spec: nvcav2beta1.ModelCacheBindingSpec{
			Identity: nvcav2beta1.ModelCacheBindingIdentity{
				Version:             "v1",
				Workflow:            nvcav2beta1.ModelCacheWorkflowRegular,
				SharingDomainDigest: "sharing-domain",
				CacheHandleDigest:   "cache-handle",
			},
			Decision: nvcav2beta1.ModelCacheBindingDecision{
				Provider:            "nvmesh",
				Provisioner:         "nvmesh-csi",
				Transition:          "regular-rox",
				RequiredAccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany},
				ProfileDigest:       catalogDigest,
				EncryptionRequired:  false,
			},
			StorageClass: nvcav2beta1.ModelCacheStorageClassSnapshot{
				Name:                "nvcf-sc",
				UID:                 "storage-class-uid",
				ReclaimPolicy:       corev1.PersistentVolumeReclaimRetain,
				ConfigurationDigest: "v1:" + catalogDigest,
			},
			Resources: nvcav2beta1.ModelCacheBindingResourceIntent{
				WriterNamespace:            "writer",
				PersistentVolumeClaimNames: []string{"writer-pvc", "reader-pvc"},
				JobNames:                   []string{"writer-job"},
			},
		},
	}

	created, err := bindings.Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)
	assert.Empty(t, created.Spec.Resources.LeaseName,
		"regular cache bindings must not claim a nonexistent Lease")

	specChange := created.DeepCopy()
	specChange.Spec.Decision.Provider = "different-provider"
	_, err = bindings.Update(ctx, specChange, metav1.UpdateOptions{})
	require.True(t, k8serrors.IsInvalid(err), "immutable spec update returned %v", err)

	statusWrite := created.DeepCopy()
	statusWrite.Spec.Decision.Provider = "ignored-by-status-subresource"
	statusWrite.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseRetiring
	statusWrite.Status.Realized = &nvcav2beta1.ModelCacheBindingRealizedState{
		ProviderDataIdentity: "provider-data-1",
		PopulationState:      nvcav2beta1.ModelCachePopulationReady,
	}
	statusUpdated, err := bindings.UpdateStatus(ctx, statusWrite, metav1.UpdateOptions{})
	require.NoError(t, err)
	assert.Equal(t, "nvmesh", statusUpdated.Spec.Decision.Provider,
		"status updates must not mutate spec")

	phaseReversal := statusUpdated.DeepCopy()
	phaseReversal.Status.Phase = nvcav2beta1.ModelCacheBindingPhaseActive
	_, err = bindings.UpdateStatus(ctx, phaseReversal, metav1.UpdateOptions{})
	require.True(t, k8serrors.IsInvalid(err), "Retiring-to-Active update returned %v", err)

	providerIdentityChange := statusUpdated.DeepCopy()
	providerIdentityChange.Status.Realized.ProviderDataIdentity = "provider-data-2"
	_, err = bindings.UpdateStatus(ctx, providerIdentityChange, metav1.UpdateOptions{})
	require.True(t, k8serrors.IsInvalid(err), "provider identity update returned %v", err)

	bareCatalogDigest := binding.DeepCopy()
	bareCatalogDigest.Name = "bare-catalog-digest"
	bareCatalogDigest.Spec.Decision.ProfileDigest = strings.Repeat("0", 64)
	_, err = bindings.Create(ctx, bareCatalogDigest, metav1.CreateOptions{})
	require.True(t, k8serrors.IsInvalid(err), "bare catalog digest returned %v", err)

	bareStorageClassDigest := binding.DeepCopy()
	bareStorageClassDigest.Name = "bare-storage-class-digest"
	bareStorageClassDigest.Spec.StorageClass.ConfigurationDigest = strings.Repeat("0", 64)
	_, err = bindings.Create(ctx, bareStorageClassDigest, metav1.CreateOptions{})
	require.True(t, k8serrors.IsInvalid(err), "bare StorageClass digest returned %v", err)
}

func Test_setupCRDs_migrateMiniService(t *testing.T) {
	b := &BackendK8sCache{
		clients: mockKubeClients(),
	}
	ctx := context.Background()

	miniserviceCRD, err := decodeCRD(miniserviceCRDData)
	require.NoError(t, err)
	miniserviceCRD.Spec.Scope = apiextv1.NamespaceScoped

	_, err = b.clients.APIExtV1.CustomResourceDefinitions().Create(ctx, miniserviceCRD, metav1.CreateOptions{})
	require.NoError(t, err)

	err = b.setupCRDs(ctx)
	require.NoError(t, err)

	gotMiniserviceCRD, err := b.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, miniserviceCRD.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, apiextv1.ClusterScoped, gotMiniserviceCRD.Spec.Scope)
}
