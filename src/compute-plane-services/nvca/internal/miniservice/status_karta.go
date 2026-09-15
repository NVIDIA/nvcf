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

package mscontroller

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	kartav1alpha1 "github.com/run-ai/karta/pkg/api/runai/v1alpha1"
	kartaresource "github.com/run-ai/karta/pkg/resource"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/kubeclients"
	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

var (
	//go:embed karta/*
	kartaDefinitions embed.FS

	kartaScheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(kartav1alpha1.AddToScheme(kartaScheme))
}

// GetKartaObjects checks if the Karta CRD is installed and returns the list of Karta objects if it is and any exist.
func GetKartaObjects(ctx context.Context, clients *kubeclients.KubeClients) (kartas []*kartav1alpha1.Karta, err error) {
	log := logf.FromContext(ctx)
	grs, err := restmapper.GetAPIGroupResources(clients.DiscoveryClient)
	if err != nil {
		log.Error(err, "Failed to get API group resources to discover Karta CRD")
		return nil, err
	}
	rm, err := restmapper.NewDiscoveryRESTMapper(grs).RESTMapping(
		schema.GroupKind{Group: kartav1alpha1.GroupVersion.Group, Kind: "Karta"},
		kartav1alpha1.GroupVersion.Version,
	)
	if err != nil {
		if meta.IsNoMatchError(err) {
			log.Info("Karta CRD not installed, using embedded Karta definitions only")
			return nil, nil
		}
		return nil, err
	}
	kartaListUns, err := clients.DynamicClient.Resource(rm.Resource).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Error(err, "Failed to list Karta objects")
		return nil, err
	}

	for _, u := range kartaListUns.Items {
		karta := &kartav1alpha1.Karta{}
		err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.UnstructuredContent(), karta)
		if err != nil {
			log.Error(err, "Failed to decode Karta object")
			return nil, err
		}
		kartas = append(kartas, karta)
	}
	return kartas, nil
}

// validateKartaGVK checks that a Karta root component GVK is addressable. An empty Group is only
// accepted for core API resources such as v1/Pod, which have no group by definition; any other
// kind with an empty group is malformed. This mirrors Karta's own root component validation.
func validateKartaGVK(gvk *kartav1alpha1.GroupVersionKind) error {
	if gvk == nil {
		return errors.New("has no root component kind")
	}
	if gvk.Version == "" || gvk.Kind == "" {
		return fmt.Errorf("gvk %q is missing version or kind", *gvk)
	}
	if gvk.Group == "" && !clientgoscheme.Scheme.Recognizes(schema.GroupVersionKind{
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}) {
		return fmt.Errorf("gvk %q has an empty group but is not a core API resource", *gvk)
	}
	return nil
}

func loadKartaDefinitions(ctx context.Context, liveKartas []*kartav1alpha1.Karta) (kartas []*kartav1alpha1.Karta, err error) {
	log := logf.FromContext(ctx)

	embeddedKartas, err := getEmbeddedKartaDefinitions()
	if err != nil {
		return nil, fmt.Errorf("get embedded karta definitions: %w", err)
	}
	kartasByGVKs := map[kartav1alpha1.GroupVersionKind]*kartav1alpha1.Karta{}
	for _, embeddedKarta := range embeddedKartas {
		gvk := embeddedKarta.Spec.StructureDefinition.RootComponent.Kind
		if err := validateKartaGVK(gvk); err != nil {
			return nil, fmt.Errorf("embedded karta %s %w", embeddedKarta.Name, err)
		}
		if _, ok := kartasByGVKs[*gvk]; ok {
			return nil, fmt.Errorf("embedded karta %s has duplicate root component kind %s", embeddedKarta.Name, gvk.Kind)
		}
		log.Info("Adding embedded Karta definition", "gvk", *gvk, "name", embeddedKarta.Name)
		kartasByGVKs[*gvk] = embeddedKarta
	}
	// Live Kartas are cluster-owned and may be installed by anything. Skip malformed ones instead
	// of failing agent startup over an object we would not use anyway.
	for i, liveKarta := range liveKartas {
		gvk := liveKarta.Spec.StructureDefinition.RootComponent.Kind
		if err := validateKartaGVK(gvk); err != nil {
			log.Error(err, "Skipping invalid Karta", "name", liveKarta.Name)
			continue
		}
		if _, ok := kartasByGVKs[*gvk]; ok {
			log.Info("Overriding embedded Karta definition", "gvk", *gvk, "name", liveKarta.Name)
			kartasByGVKs[*gvk] = liveKartas[i]
		}
	}
	kartas = make([]*kartav1alpha1.Karta, 0, len(kartasByGVKs))
	for _, karta := range kartasByGVKs {
		kartas = append(kartas, karta)
	}
	sort.Slice(kartas, func(i, j int) bool {
		return kartas[i].Name < kartas[j].Name
	})
	return kartas, nil
}

func getEmbeddedKartaDefinitions() (kartas []*kartav1alpha1.Karta, err error) {
	decoder := serializer.NewCodecFactory(kartaScheme).UniversalDeserializer()
	if err := fs.WalkDir(kartaDefinitions, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := kartaDefinitions.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read karta definition %s: %w", path, err)
		}
		karta := &kartav1alpha1.Karta{}
		if _, _, err = decoder.Decode(content, nil, karta); err != nil {
			return fmt.Errorf("failed to decode karta definition %s: %w", path, err)
		}
		kartas = append(kartas, karta)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to walk karta definitions: %w", err)
	}
	return kartas, nil
}

func kartaGVKToSchemaGVK(gvk kartav1alpha1.GroupVersionKind) schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}
}

func (r *Reconciler) newKartaDefinedObjectStatusChecker(karta *kartav1alpha1.Karta) (checkStatusFunc, error) {
	gvk := karta.Spec.StructureDefinition.RootComponent.Kind
	if gvk == nil {
		return nil, fmt.Errorf("karta %s has no root component kind", karta.Name)
	}
	statusDef := karta.Spec.StructureDefinition.RootComponent.StatusDefinition
	if statusDef == nil {
		return nil, fmt.Errorf("karta %s has no root component status definition", karta.Name)
	}
	statusEntries := statusDef.StatusMappings.Entries()

	// Ensure at least running and failed entry types are present.
	requiredStatusEntries := map[kartav1alpha1.ResourceStatus]bool{
		kartav1alpha1.RunningStatus: false,
		kartav1alpha1.FailedStatus:  false,
	}
	for _, entry := range statusEntries {
		if _, ok := requiredStatusEntries[entry.Status]; ok && len(entry.Matchers) > 0 {
			requiredStatusEntries[entry.Status] = true
		}
	}
	missingStatusEntries := []kartav1alpha1.ResourceStatus{}
	for status, present := range requiredStatusEntries {
		if !present {
			missingStatusEntries = append(missingStatusEntries, status)
		}
	}
	if len(missingStatusEntries) > 0 {
		return nil, fmt.Errorf("karta %s has no required status entries: %v", karta.Name, missingStatusEntries)
	}

	return func(ctx context.Context, obj client.Object, _ *nvcav2beta1.ICMSRequest) (ObjectStatus, error) {
		kartaRF := kartaresource.NewComponentFactoryFromObject(karta, obj)

		rootComponent, err := kartaRF.GetRootComponent()
		if err != nil {
			return ObjectStatus{}, fmt.Errorf("failed to get root component: %w", err)
		}

		rootComponentStatus, err := rootComponent.GetStatus(ctx)
		if err != nil {
			return ObjectStatus{}, fmt.Errorf("failed to extract root component status: %w", err)
		}

		objStatus := ObjectStatus{
			Object: obj,
		}
		if len(rootComponentStatus.MatchedStatuses) == 0 {
			objStatus.Status = "initializing"
			objStatus.Pending = true
		} else {
			// There may be multiple statuses matched, so find the highest ranked one by severity.
			sort.Slice(rootComponentStatus.MatchedStatuses, func(i, j int) bool {
				return kartaStatusRanks[rootComponentStatus.MatchedStatuses[i]] <
					kartaStatusRanks[rootComponentStatus.MatchedStatuses[j]]
			})
			kartaStatus := rootComponentStatus.MatchedStatuses[len(rootComponentStatus.MatchedStatuses)-1]
			setKartaStatusOnObjectStatus(&objStatus, kartaStatus)
		}

		abnormalEvents, isError := r.getUnexpectedEventsForObject(ctx, obj)
		objStatus.AbnormalEvents = abnormalEvents
		if isError && !objStatus.TerminalBad {
			objStatus = objStatus.toTerminalEventStatus()
		}

		return objStatus, nil
	}, nil
}

// kartaStatusRanks maps a karta status to a rank, used to determine the highest ranked status.
// Higher rank means higher severity.
var kartaStatusRanks = map[kartav1alpha1.ResourceStatus]int{
	kartav1alpha1.UndefinedStatus:    0,
	kartav1alpha1.InitializingStatus: 1,
	kartav1alpha1.RunningStatus:      2,
	kartav1alpha1.CompletedStatus:    3,
	kartav1alpha1.DegradedStatus:     4,
	kartav1alpha1.FailedStatus:       5,
}

func setKartaStatusOnObjectStatus(objStatus *ObjectStatus, status kartav1alpha1.ResourceStatus) {
	switch status {
	case kartav1alpha1.UndefinedStatus:
		objStatus.Status = "undefined"
		objStatus.Pending = true
	case kartav1alpha1.RunningStatus:
		objStatus.Status = statusRunning
	case kartav1alpha1.CompletedStatus:
		objStatus.Status = statusSucceeded
	case kartav1alpha1.FailedStatus:
		objStatus.Status = statusFailed
		objStatus.TerminalBad = true
	case kartav1alpha1.DegradedStatus:
		objStatus.Status = statusDegradedWorker
		objStatus.TerminalBad = true
	default:
		objStatus.Status = statusStarting
		objStatus.Pending = true
	}
}
