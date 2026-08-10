// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/run-ai/karta/pkg/api/runai/v1alpha1"
	"github.com/run-ai/karta/pkg/jq/execution"
)

type ComponentReader interface {
	ExtractPodTemplateSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodTemplateSpec, error)
	ExtractPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodSpec, error)
	ExtractPodMetadata(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]metav1.ObjectMeta, error)
	ExtractFragmentedPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]FragmentedPodSpec, error)
	ExtractScale(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]Scale, error)
	ExtractStatus(ctx context.Context, definition v1alpha1.ComponentDefinition) (*Status, error)
	ExtractInstanceIds(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]string, error)
	GetObject() (map[string]interface{}, error)
}

type ComponentWriter interface {
	UpdatePodTemplateSpec(ctx context.Context, definition v1alpha1.ComponentDefinition, podTemplateSpecs []corev1.PodTemplateSpec) error
	UpdatePodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition, podSpecs []corev1.PodSpec) error
	UpdatePodMetadata(ctx context.Context, definition v1alpha1.ComponentDefinition, podMetadata []metav1.ObjectMeta) error
	UpdateFragmentedPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition, fragmentedPodSpecs []FragmentedPodSpec) error
}

//go:generate mockgen -source=component_factory.go -destination=accessor_mock.go -package=resource ComponentAccessor
type ComponentAccessor interface {
	ComponentReader
	ComponentWriter
}

type ComponentFactory struct {
	karta      *v1alpha1.Karta
	accessor ComponentAccessor

	componentDefinitionsByName map[string]v1alpha1.ComponentDefinition
}

// NewComponentFactory creates a new Karta-based component factory
func NewComponentFactory(karta *v1alpha1.Karta, accessor ComponentAccessor) *ComponentFactory {
	definitionsByName := make(map[string]v1alpha1.ComponentDefinition)

	// Create single slice with all components (root + children)
	allDefinitions := append(karta.Spec.StructureDefinition.ChildComponents, karta.Spec.StructureDefinition.RootComponent)
	for _, componentDefinition := range allDefinitions {
		definitionsByName[componentDefinition.Name] = componentDefinition
	}

	return &ComponentFactory{
		karta:                        karta,
		accessor:                   accessor,
		componentDefinitionsByName: definitionsByName,
	}
}

// NewComponentFactoryFromObject creates a new Karta-based component factory from a Kubernetes object
func NewComponentFactoryFromObject(karta *v1alpha1.Karta, object client.Object) *ComponentFactory {
	jqRunner := execution.NewDefaultRunner(object)
	accessor := NewAccessor(jqRunner)
	return NewComponentFactory(karta, accessor)
}

// GetComponent retrieves a component by name
func (f *ComponentFactory) GetComponent(name string) (*Component, error) {
	definition, exists := f.componentDefinitionsByName[name]
	if !exists {
		return nil, fmt.Errorf("component %s not found", name)
	}

	return &Component{
		name:       name,
		definition: definition,
		accessor:   f.accessor,
	}, nil
}

// GetRootComponent retrieves the root component
func (f *ComponentFactory) GetRootComponent() (*Component, error) {
	if f.karta == nil {
		return nil, fmt.Errorf("karta is nil")
	}

	return f.GetComponent(f.karta.Spec.StructureDefinition.RootComponent.Name)
}

// GetChildComponents retrieves all child components
func (f *ComponentFactory) GetChildComponents() ([]*Component, error) {
	if f.karta == nil {
		return nil, fmt.Errorf("karta is nil")
	}

	childComponents := make([]*Component, 0, len(f.karta.Spec.StructureDefinition.ChildComponents))
	for _, childDefinition := range f.karta.Spec.StructureDefinition.ChildComponents {
		component, err := f.GetComponent(childDefinition.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to get child component %s: %w", childDefinition.Name, err)
		}
		childComponents = append(childComponents, component)
	}

	return childComponents, nil
}

func (f *ComponentFactory) GetResource() (client.Object, error) {
	object, err := f.accessor.GetObject()
	if err != nil {
		return nil, fmt.Errorf("failed to get updated data: %w", err)
	}
	u := &unstructured.Unstructured{Object: object}
	if err := validateKubernetesObject(u); err != nil {
		return nil, fmt.Errorf("invalid Kubernetes object: %w", err)
	}
	return u, nil
}

func (f *ComponentFactory) IsContainSpecDefinition() (bool, error) {
	components, err := f.GetChildComponents()
	if err != nil {
		return false, fmt.Errorf("failed to get child components: %w", err)
	}
	rootComponent, err := f.GetRootComponent()
	if err != nil {
		return false, fmt.Errorf("failed to get root component: %w", err)
	}
	components = append(components, rootComponent)
	for _, component := range components {
		if isComponentHasSpecDefinition(component.Definition()) {
			return true, nil
		}
	}
	return false, nil
}

func isComponentHasSpecDefinition(componentDefinition v1alpha1.ComponentDefinition) bool {
	if componentDefinition.SpecDefinition == nil {
		return false
	}

	return componentDefinition.SpecDefinition.PodTemplateSpecPath != nil ||
		componentDefinition.SpecDefinition.PodSpecPath != nil ||
		componentDefinition.SpecDefinition.FragmentedPodSpecDefinition != nil
}

func validateKubernetesObject(u *unstructured.Unstructured) error {
	gvk := u.GroupVersionKind()
	if gvk.Group == "" && gvk.Version == "" { // Core groups might have empty Group, but need Version
		return fmt.Errorf("missing apiVersion")
	}
	if gvk.Kind == "" {
		return fmt.Errorf("missing kind")
	}
	if u.GetName() == "" && u.GetGenerateName() == "" {
		return fmt.Errorf("missing metadata.name or metadata.generateName")
	}
	return nil
}
