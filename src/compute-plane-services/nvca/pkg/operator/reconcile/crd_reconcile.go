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
	_ "embed"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	//go:embed manifests/nvcf.nvidia.io_storagerequests_crd.yaml
	storageRequestsCRDData []byte
	//go:embed manifests/nvcf.nvidia.io_miniservices_crd.yaml
	miniserviceCRDData []byte
	//go:embed manifests/nvsnap.nvcf.nvidia.io_nvsnapfunctionstates_crd.yaml
	nvsnapFunctionStateCRDData []byte
)

const (
	StorageRequestCRDName      = "storagerequests.nvca.nvcf.nvidia.io"
	MiniServicesCRDName        = "miniservices.nvca.nvcf.nvidia.io"
	ICMSRequestCRDName         = "icmsrequests.nvca.nvcf.nvidia.io"
	NVCFBackendCRDName         = "nvcfbackends.nvcf.nvidia.io"
	NvSnapFunctionStateCRDName = "nvsnapfunctionstates.nvsnap.nvcf.nvidia.io"
)

func (c *BackendK8sCache) setupCRDs(ctx context.Context) error {
	log := core.GetLogger(ctx)

	storageReqCRD, err := decodeCRD(storageRequestsCRDData)
	if err != nil {
		return fmt.Errorf("make StorageRequest CRD: %v", err)
	}
	miniserviceCRD, err := decodeCRD(miniserviceCRDData)
	if err != nil {
		return fmt.Errorf("make MiniService CRD: %v", err)
	}
	nvsnapFunctionStateCRD, err := decodeCRD(nvsnapFunctionStateCRDData)
	if err != nil {
		return fmt.Errorf("make NvSnapFunctionState CRD: %v", err)
	}

	// Get the NVCFBackend CRD to use as owner reference.
	// This ensures operator-managed CRDs are garbage collected when Helm uninstalls.
	var ownerRef *metav1.OwnerReference
	nvcfBackendCRD, err := c.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, NVCFBackendCRDName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Warn("Failed to get NVCFBackend CRD for owner reference, operator-managed CRDs will not be garbage collected on uninstall")
	} else {
		ownerRef = &metav1.OwnerReference{
			APIVersion: "apiextensions.k8s.io/v1",
			Kind:       "CustomResourceDefinition",
			Name:       nvcfBackendCRD.Name,
			UID:        nvcfBackendCRD.UID,
		}
	}

	// The miniservice CRD was migrated to be cluster-scoped,
	// but the scope field is immutable. Since the CRD is not in use yet,
	// the operator can migrate it by deleting the existing namespace-scoped CRD.
	// This migration is temporary and can be removed once miniservices
	// are in use.
	msCRD, err := c.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, miniserviceCRD.Name, metav1.GetOptions{})
	if err == nil && msCRD.Spec.Scope == apiextv1.NamespaceScoped {
		err = c.clients.APIExtV1.CustomResourceDefinitions().Delete(ctx, miniserviceCRD.Name, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to migrate miniservice CRD: %v", err)
		}
	}
	for _, crdObj := range []*apiextv1.CustomResourceDefinition{
		storageReqCRD,
		miniserviceCRD,
		makeICMSRequestCRD(),
		nvsnapFunctionStateCRD,
	} {
		// Set owner reference so CRDs are garbage collected when NVCFBackend CRD is deleted
		if ownerRef != nil {
			crdObj.OwnerReferences = []metav1.OwnerReference{*ownerRef}
		}
		if err := c.applyCRD(ctx, crdObj); err != nil {
			return fmt.Errorf("apply CRD %s: %v", crdObj.Name, err)
		}
	}
	return nil
}

type requestCRDConfig struct {
	name            string
	group           string
	names           apiextv1.CustomResourceDefinitionNames
	version         string
	actionColName   string
	actionDesc      string
	statusDesc      string
	functionVerDesc string
}

func makeRequestCRD(cfg requestCRDConfig) *apiextv1.CustomResourceDefinition {
	preserve := true
	return &apiextv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: cfg.name},
		Spec: apiextv1.CustomResourceDefinitionSpec{
			Group: cfg.group,
			Names: cfg.names,
			Scope: apiextv1.NamespaceScoped,
			Versions: []apiextv1.CustomResourceDefinitionVersion{
				{
					Name:    cfg.version,
					Served:  true,
					Storage: true,
					Subresources: &apiextv1.CustomResourceSubresources{
						Status: &apiextv1.CustomResourceSubresourceStatus{},
					},
					AdditionalPrinterColumns: []apiextv1.CustomResourceColumnDefinition{
						{Name: cfg.actionColName, JSONPath: ".spec.action", Type: "string", Description: cfg.actionDesc},
						{Name: "status", JSONPath: ".status.requestStatus", Type: "string", Description: cfg.statusDesc},
						{Name: "nca_id", JSONPath: ".spec.ncaId", Type: "string", Description: "NVIDIA CloudAccount ID of the " + cfg.names.Kind + " in NVCF"},
						{Name: "Age", JSONPath: ".metadata.creationTimestamp", Type: "date", Description: "Age of this resource"},
						{Name: "function_version_id", JSONPath: ".spec.functionVersionId", Type: "string", Description: cfg.functionVerDesc},
					},
					Schema: &apiextv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextv1.JSONSchemaProps{
							Type:     "object",
							Required: []string{"spec"},
							Properties: map[string]apiextv1.JSONSchemaProps{
								"spec":   {Type: "object", XPreserveUnknownFields: &preserve},
								"status": {Type: "object", XPreserveUnknownFields: &preserve},
							},
						},
					},
				},
			},
		},
	}
}

func makeICMSRequestCRD() *apiextv1.CustomResourceDefinition {
	return makeRequestCRD(requestCRDConfig{
		name:  ICMSRequestCRDName,
		group: "nvca.nvcf.nvidia.io",
		names: apiextv1.CustomResourceDefinitionNames{
			Plural: "icmsrequests", Singular: "icmsrequest", ShortNames: []string{"ir"},
			Kind: "ICMSRequest", ListKind: "ICMSRequestList",
		},
		version:         "v2beta1",
		actionColName:   "action",
		actionDesc:      "Type of the ICMS Request",
		statusDesc:      "Status of the ICMSRequest CR on the backend",
		functionVerDesc: "FunctionVersionId of the ICMS Request in NVCF",
	})
}

func (c *BackendK8sCache) applyCRD(ctx context.Context, crdObj *apiextv1.CustomResourceDefinition) error {
	log := core.GetLogger(ctx)

	crdCur, err := c.clients.APIExtV1.CustomResourceDefinitions().Get(ctx, crdObj.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err := c.clients.APIExtV1.CustomResourceDefinitions().Create(ctx, crdObj, metav1.CreateOptions{})
			if err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("failed to create CRD %v, err: %v", crdObj.Name, err)
			}
		} else {
			return fmt.Errorf("failed to get CRD %v, err: %v", crdObj.Name, err)
		}
	} else {
		// object exists, update
		if objectsEqual(crdObj.Spec, crdCur.Spec) {
			log.Infof("CRD %v is already at the updated spec, skip sync", crdObj.Name)
		} else {
			// set ResourceVersion & UUID for Update
			crdObj.ObjectMeta.ResourceVersion = crdCur.ObjectMeta.ResourceVersion //nolint:staticcheck
			crdObj.ObjectMeta.UID = crdCur.ObjectMeta.UID                         //nolint:staticcheck
			_, err := c.clients.APIExtV1.CustomResourceDefinitions().Update(ctx, crdObj, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update CRD %v, err: %v", crdObj.Name, err)
			}
		}
	}
	return nil
}

func decodeCRD(data []byte) (*apiextv1.CustomResourceDefinition, error) {
	scheme := runtime.NewScheme()
	if err := apiextv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	crd := &apiextv1.CustomResourceDefinition{}
	_, _, err := serializer.NewCodecFactory(scheme).
		UniversalDeserializer().
		Decode(data, nil, crd)
	return crd, err
}
