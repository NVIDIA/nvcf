// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/utils/ptr"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/run-ai/karta/pkg/api/runai/v1alpha1"
	"github.com/run-ai/karta/pkg/jq/execution"
)

// DefinitionNotFoundError represents an error when a requested definition is not found
type DefinitionNotFoundError string

func (e DefinitionNotFoundError) Error() string {
	return string(e)
}

// Accessor implements extraction and updating of resource data using jq.Runner
type Accessor struct {
	jqRunner execution.Runner
}

func NewAccessor(jqRunner execution.Runner) *Accessor {
	return &Accessor{
		jqRunner: jqRunner,
	}
}

// GetObject returns the object as a map[string]interface{}
func (a *Accessor) GetObject() (map[string]interface{}, error) {
	object, err := a.jqRunner.GetObject()
	if err != nil {
		return nil, err
	}
	objectMap, ok := object.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("object is not a map[string]interface{}")
	}
	return objectMap, nil
}

func (a *Accessor) ExtractPodTemplateSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodTemplateSpec, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.PodTemplateSpecPath == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod template spec definition", definition.Name))
	}

	var podTemplateSpec []corev1.PodTemplateSpec
	err := extract(ctx, definition.SpecDefinition.PodTemplateSpecPath, a.jqRunner, &podTemplateSpec)

	return podTemplateSpec, err
}

func (a *Accessor) ExtractPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]corev1.PodSpec, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.PodSpecPath == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod spec definition", definition.Name))
	}

	var podSpec []corev1.PodSpec
	err := extract(ctx, definition.SpecDefinition.PodSpecPath, a.jqRunner, &podSpec)

	return podSpec, err
}

func (a *Accessor) ExtractPodMetadata(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]metav1.ObjectMeta, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.MetadataPath == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod metadata definition", definition.Name))
	}

	var podMetadata []metav1.ObjectMeta
	err := extract(ctx, definition.SpecDefinition.MetadataPath, a.jqRunner, &podMetadata)

	return podMetadata, err
}

func (a *Accessor) ExtractScale(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]Scale, error) {
	if definition.ScaleDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have scale definition", definition.Name))
	}

	var (
		replicas    []*int32
		minReplicas []*int32
		maxReplicas []*int32
	)

	scaleCount := 0

	if err := extract(ctx, definition.ScaleDefinition.ReplicasPath, a.jqRunner, &replicas); err != nil {
		return nil, err
	}
	scaleCount = max(scaleCount, len(replicas))

	if err := extract(ctx, definition.ScaleDefinition.MinReplicasPath, a.jqRunner, &minReplicas); err != nil {
		return nil, err
	}
	scaleCount = max(scaleCount, len(minReplicas))

	if err := extract(ctx, definition.ScaleDefinition.MaxReplicasPath, a.jqRunner, &maxReplicas); err != nil {
		return nil, err
	}
	scaleCount = max(scaleCount, len(maxReplicas))

	scales := make([]Scale, scaleCount)
	for i := 0; i < scaleCount; i++ {
		scales[i] = Scale{
			Replicas:    safeGetByIndex(replicas, i),
			MaxReplicas: safeGetByIndex(maxReplicas, i),
			MinReplicas: safeGetByIndex(minReplicas, i),
		}
	}

	return scales, nil
}

func (a *Accessor) ExtractFragmentedPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]FragmentedPodSpec, error) {
	if definition.SpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.FragmentedPodSpecDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have fragmented pod spec definition", definition.Name))
	}

	fragmentedDefinition := definition.SpecDefinition.FragmentedPodSpecDefinition

	var (
		schedulerNameResults     []string
		labelsResults            []map[string]string
		annotationsResults       []map[string]string
		resourcesResults         []*corev1.ResourceRequirements
		resourceClaimsResults    [][]corev1.PodResourceClaim
		podAffinityResults       []*corev1.PodAffinity
		nodeAffinityResults      []*corev1.NodeAffinity
		containersResults        [][]corev1.Container
		containerResults         []*corev1.Container
		priorityClassNameResults []string
		imageResults             []string
	)

	specCount := 0

	if err := extract(ctx, fragmentedDefinition.SchedulerNamePath, a.jqRunner, &schedulerNameResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(schedulerNameResults))

	if err := extract(ctx, fragmentedDefinition.LabelsPath, a.jqRunner, &labelsResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(labelsResults))

	if err := extract(ctx, fragmentedDefinition.AnnotationsPath, a.jqRunner, &annotationsResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(annotationsResults))

	if err := extract(ctx, fragmentedDefinition.ResourcesPath, a.jqRunner, &resourcesResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(resourcesResults))

	if err := extract(ctx, fragmentedDefinition.ResourceClaimsPath, a.jqRunner, &resourceClaimsResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(resourceClaimsResults))

	if err := extract(ctx, fragmentedDefinition.PodAffinityPath, a.jqRunner, &podAffinityResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(podAffinityResults))

	if err := extract(ctx, fragmentedDefinition.NodeAffinityPath, a.jqRunner, &nodeAffinityResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(nodeAffinityResults))

	if err := extract(ctx, fragmentedDefinition.ContainersPath, a.jqRunner, &containersResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(containersResults))

	if err := extract(ctx, fragmentedDefinition.ContainerPath, a.jqRunner, &containerResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(containerResults))

	if err := extract(ctx, fragmentedDefinition.PriorityClassNamePath, a.jqRunner, &priorityClassNameResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(priorityClassNameResults))

	if err := extract(ctx, fragmentedDefinition.ImagePath, a.jqRunner, &imageResults); err != nil {
		return nil, err
	}
	specCount = max(specCount, len(imageResults))

	fragmentedSpecs := make([]FragmentedPodSpec, specCount)
	for i := 0; i < specCount; i++ {
		fragmentedSpecs[i] = FragmentedPodSpec{
			SchedulerName:     safeGetByIndex(schedulerNameResults, i),
			Labels:            safeGetByIndex(labelsResults, i),
			Annotations:       safeGetByIndex(annotationsResults, i),
			Resources:         safeGetByIndex(resourcesResults, i),
			ResourceClaims:    safeGetByIndex(resourceClaimsResults, i),
			PodAffinity:       safeGetByIndex(podAffinityResults, i),
			NodeAffinity:      safeGetByIndex(nodeAffinityResults, i),
			Containers:        safeGetByIndex(containersResults, i),
			Container:         safeGetByIndex(containerResults, i),
			PriorityClassName: safeGetByIndex(priorityClassNameResults, i),
			Image:             safeGetByIndex(imageResults, i),
		}
	}

	return fragmentedSpecs, nil
}

// ExtractStatus evaluates the status of the component based on the status definition.
func (a *Accessor) ExtractStatus(ctx context.Context, definition v1alpha1.ComponentDefinition) (*Status, error) {
	if definition.StatusDefinition == nil {
		return nil, DefinitionNotFoundError(fmt.Sprintf("component %s does not have status definition", definition.Name))
	}

	statusDef := definition.StatusDefinition

	var phase *string
	if statusDef.PhaseDefinition != nil {
		var phases []string
		if err := extract(ctx, &statusDef.PhaseDefinition.Path, a.jqRunner, &phases); err != nil {
			return nil, fmt.Errorf("failed to extract phase: %w", err)
		}
		if len(phases) > 0 {
			phase = &phases[0]
		}
	}

	conditions, err := a.extractConditions(ctx, statusDef.ConditionsDefinition)
	if err != nil {
		return nil, err
	}

	matchedStatuses, err := matchStatus(ctx, a.jqRunner, phase, conditions, statusDef.StatusMappings)
	if err != nil {
		return nil, fmt.Errorf("failed to match status: %w", err)
	}

	status := Status{
		Phase:           phase,
		Conditions:      conditions,
		MatchedStatuses: matchedStatuses,
	}

	return &status, nil
}

func (a *Accessor) extractConditions(ctx context.Context, condDef *v1alpha1.ConditionsDefinition) ([]Condition, error) {
	if condDef == nil {
		return []Condition{}, nil
	}

	var extractedRawConditions [][]map[string]any
	if err := extract(ctx, &condDef.Path, a.jqRunner, &extractedRawConditions); err != nil {
		return nil, fmt.Errorf("failed to extract conditions: %w", err)
	}

	// As Condition path is a path to a list of conditions, extract returns a slice of slice in size one/zero,
	// so we need to flatten it
	rawConditions := lo.Flatten(extractedRawConditions)

	conditions := make([]Condition, 0, len(rawConditions))
	for _, condMap := range rawConditions {
		cond := Condition{}

		if typeVal, ok := condMap[condDef.TypeFieldName]; ok {
			if typeStr, ok := typeVal.(string); ok {
				cond.Type = typeStr
			}
		}

		if statusVal, ok := condMap[condDef.StatusFieldName]; ok {
			if statusStr, ok := statusVal.(string); ok {
				cond.Status = &statusStr
			}
		}

		if condDef.MessageFieldName != nil {
			if msgVal, ok := condMap[*condDef.MessageFieldName]; ok {
				if msgStr, ok := msgVal.(string); ok {
					cond.Message = msgStr
				}
			}
		}

		if condDef.ReasonFieldName != nil {
			if reasonVal, ok := condMap[*condDef.ReasonFieldName]; ok {
				if reasonStr, ok := reasonVal.(string); ok {
					cond.Reason = &reasonStr
				}
			}
		}

		conditions = append(conditions, cond)
	}
	return conditions, nil
}

func (a *Accessor) ExtractInstanceIds(ctx context.Context, definition v1alpha1.ComponentDefinition) ([]string, error) {
	if definition.InstanceIdPath == nil {
		return nil, DefinitionNotFoundError("no instance id path defined")
	}

	var instanceIds []string
	err := extract(ctx, definition.InstanceIdPath, a.jqRunner, &instanceIds)
	if err != nil {
		return nil, err
	}

	// Validate all instance ids are not empty
	if lo.Contains(instanceIds, "") {
		return nil, fmt.Errorf("instance id path contained empty string values [%s]", strings.Join(instanceIds, ","))
	}

	return instanceIds, nil
}

func (a *Accessor) UpdatePodTemplateSpec(ctx context.Context, definition v1alpha1.ComponentDefinition, podTemplateSpecs []corev1.PodTemplateSpec) error {
	if definition.SpecDefinition == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.PodTemplateSpecPath == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod template spec definition", definition.Name))
	}

	return a.assign(ctx, definition, *definition.SpecDefinition.PodTemplateSpecPath, lo.Map(podTemplateSpecs, func(podTemplateSpec corev1.PodTemplateSpec, _ int) any { return podTemplateSpec }))
}

func (a *Accessor) UpdatePodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition, podSpecs []corev1.PodSpec) error {
	if definition.SpecDefinition == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.PodSpecPath == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod spec definition", definition.Name))
	}

	return a.assign(ctx, definition, *definition.SpecDefinition.PodSpecPath, lo.Map(podSpecs, func(podSpec corev1.PodSpec, _ int) any { return podSpec }))
}

func (a *Accessor) UpdatePodMetadata(ctx context.Context, definition v1alpha1.ComponentDefinition, podMetadata []metav1.ObjectMeta) error {
	if definition.SpecDefinition == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.MetadataPath == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have pod metadata definition", definition.Name))
	}
	return a.assign(ctx, definition, *definition.SpecDefinition.MetadataPath, lo.Map(podMetadata, func(podMetadata metav1.ObjectMeta, _ int) any { return podMetadata }))
}

func (a *Accessor) UpdateFragmentedPodSpec(ctx context.Context, definition v1alpha1.ComponentDefinition, fragmentedPodSpecs []FragmentedPodSpec) error {
	if definition.SpecDefinition == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have spec definition", definition.Name))
	}

	if definition.SpecDefinition.FragmentedPodSpecDefinition == nil {
		return DefinitionNotFoundError(fmt.Sprintf("component %s does not have fragmented pod spec definition", definition.Name))
	}

	fragmentedDef := definition.SpecDefinition.FragmentedPodSpecDefinition

	// String fields
	if err := a.updateStringField(ctx, definition, fragmentedDef.SchedulerNamePath, fragmentedPodSpecs, func(s FragmentedPodSpec) string { return s.SchedulerName }); err != nil {
		return fmt.Errorf("failed to update scheduler name: %w", err)
	}
	if err := a.updateStringField(ctx, definition, fragmentedDef.PriorityClassNamePath, fragmentedPodSpecs, func(s FragmentedPodSpec) string { return s.PriorityClassName }); err != nil {
		return fmt.Errorf("failed to update priority class name: %w", err)
	}
	if err := a.updateStringField(ctx, definition, fragmentedDef.ImagePath, fragmentedPodSpecs, func(s FragmentedPodSpec) string { return s.Image }); err != nil {
		return fmt.Errorf("failed to update image: %w", err)
	}

	// Map fields
	if err := updateMapField(a, ctx, definition, fragmentedDef.LabelsPath, fragmentedPodSpecs, func(s FragmentedPodSpec) map[string]string { return s.Labels }); err != nil {
		return fmt.Errorf("failed to update labels: %w", err)
	}
	if err := updateMapField(a, ctx, definition, fragmentedDef.AnnotationsPath, fragmentedPodSpecs, func(s FragmentedPodSpec) map[string]string { return s.Annotations }); err != nil {
		return fmt.Errorf("failed to update annotations: %w", err)
	}

	// Pointer fields
	if err := updateStructPointerField(a, ctx, definition, fragmentedDef.ResourcesPath, fragmentedPodSpecs, func(s FragmentedPodSpec) *corev1.ResourceRequirements { return s.Resources }); err != nil {
		return fmt.Errorf("failed to update resources: %w", err)
	}
	if err := updateStructPointerField(a, ctx, definition, fragmentedDef.PodAffinityPath, fragmentedPodSpecs, func(s FragmentedPodSpec) *corev1.PodAffinity { return s.PodAffinity }); err != nil {
		return fmt.Errorf("failed to update pod affinity: %w", err)
	}
	if err := updateStructPointerField(a, ctx, definition, fragmentedDef.NodeAffinityPath, fragmentedPodSpecs, func(s FragmentedPodSpec) *corev1.NodeAffinity { return s.NodeAffinity }); err != nil {
		return fmt.Errorf("failed to update node affinity: %w", err)
	}
	if err := updateStructPointerField(a, ctx, definition, fragmentedDef.ContainerPath, fragmentedPodSpecs, func(s FragmentedPodSpec) *corev1.Container { return s.Container }); err != nil {
		return fmt.Errorf("failed to update container: %w", err)
	}

	// Slice fields
	if err := updateSliceField(a, ctx, definition, fragmentedDef.ResourceClaimsPath, fragmentedPodSpecs, func(s FragmentedPodSpec) []corev1.PodResourceClaim { return s.ResourceClaims }); err != nil {
		return fmt.Errorf("failed to update resource claims: %w", err)
	}
	if err := updateSliceField(a, ctx, definition, fragmentedDef.ContainersPath, fragmentedPodSpecs, func(s FragmentedPodSpec) []corev1.Container { return s.Containers }); err != nil {
		return fmt.Errorf("failed to update containers: %w", err)
	}

	return nil
}

func (a *Accessor) updateField(ctx context.Context, def v1alpha1.ComponentDefinition, path *string, values []any, isEmpty func(any) bool) error {
	if path != nil {
		// Skip assignment if all values are empty/nil to avoid writing null
		// into the JSON.
		allEmpty := true
		for _, v := range values {
			if !isEmpty(v) {
				allEmpty = false
				break
			}
		}
		if allEmpty {
			return nil
		}
		return a.assign(ctx, def, *path, values)
	}
	for _, v := range values {
		if !isEmpty(v) {
			return fmt.Errorf("path is not defined and values are not empty")
		}
	}
	return nil
}

func (a *Accessor) updateStringField(ctx context.Context, def v1alpha1.ComponentDefinition, path *string, specs []FragmentedPodSpec, getter func(FragmentedPodSpec) string) error {
	values := lo.Map(specs, func(s FragmentedPodSpec, _ int) any { return getter(s) })
	return a.updateField(ctx, def, path, values, func(v any) bool { return v.(string) == "" })
}

func updateMapField[K comparable, V any](a *Accessor, ctx context.Context, def v1alpha1.ComponentDefinition, path *string, specs []FragmentedPodSpec, getter func(FragmentedPodSpec) map[K]V) error {
	values := lo.Map(specs, func(s FragmentedPodSpec, _ int) any { return getter(s) })
	return a.updateField(ctx, def, path, values, func(v any) bool { return len(v.(map[K]V)) == 0 })
}

func updateStructPointerField[T any](a *Accessor, ctx context.Context, def v1alpha1.ComponentDefinition, path *string, specs []FragmentedPodSpec, getter func(FragmentedPodSpec) *T) error {
	values := lo.Map(specs, func(s FragmentedPodSpec, _ int) any { return getter(s) })
	return a.updateField(ctx, def, path, values, func(v any) bool { return v.(*T) == nil })
}

func updateSliceField[T any](a *Accessor, ctx context.Context, def v1alpha1.ComponentDefinition, path *string, specs []FragmentedPodSpec, getter func(FragmentedPodSpec) []T) error {
	values := lo.Map(specs, func(s FragmentedPodSpec, _ int) any { return getter(s) })
	return a.updateField(ctx, def, path, values, func(v any) bool { return len(v.([]T)) == 0 })
}

func (a *Accessor) assign(ctx context.Context, definition v1alpha1.ComponentDefinition, path string, values []any) error {
	// If instance id path is defined, use assign zip as the expression is an array expression (each coordinate per instance)
	if definition.InstanceIdPath != nil {
		return a.jqRunner.AssignZip(ctx, path, values)
	} else {
		return a.jqRunner.Assign(ctx, path, values[0])
	}
}

func extract[T any](ctx context.Context, path *string, accessor execution.Runner, out *[]T) error {
	if path == nil {
		return nil
	}

	results, err := accessor.Evaluate(ctx, *path)
	if err != nil {
		return err
	}

	converted, err := safeConvertSlice[T](results)
	if err != nil {
		return err
	}

	*out = converted
	return nil
}

// safeGetByIndex Generic function for safely retrieving a slice element.
// Returns zero value if slice is nil or index is out of range
func safeGetByIndex[T any](slice []T, index int) T {
	var zero T

	if slice == nil {
		return zero
	}

	if index < 0 || index >= len(slice) {
		return zero
	}

	return slice[index]
}

// safeConvertSlice Generic type conversion for slice objects
func safeConvertSlice[T any](slice []any) ([]T, error) {
	if slice == nil {
		return nil, nil
	}

	convertedResults := make([]T, len(slice))
	for i, object := range slice {
		// First try direct type assertion (for simple types)
		if converted, ok := object.(T); ok {
			convertedResults[i] = converted
			continue
		}

		// For complex types (structs), use JSON marshaling/unmarshaling
		var converted T
		if err := convertViaJSON(object, &converted); err != nil {
			return nil, fmt.Errorf("failed to convert object at index %d to type %T: %w", i, converted, err)
		}
		convertedResults[i] = converted
	}

	return convertedResults, nil
}

// convertViaJSON converts between types using JSON marshaling
func convertViaJSON(src any, dst any) error {
	jsonBytes, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("failed to marshal source: %w", err)
	}

	if err := json.Unmarshal(jsonBytes, dst); err != nil {
		return fmt.Errorf("failed to unmarshal to destination: %w", err)
	}

	return nil
}

func matchStatus(ctx context.Context, jqRunner execution.Runner, phase *string, conditions []Condition, mappings v1alpha1.StatusMappings) ([]v1alpha1.ResourceStatus, error) {
	conditionsMap := make(map[string]Condition, len(conditions))
	for _, cond := range conditions {
		conditionsMap[cond.Type] = cond
	}

	matchedStatuses := make([]v1alpha1.ResourceStatus, 0)
	for _, entry := range mappings.Entries() {
		matched, err := evaluateMatchers(ctx, jqRunner, phase, conditionsMap, entry.Matchers)
		if err != nil {
			return nil, err
		}
		if matched {
			matchedStatuses = append(matchedStatuses, entry.Status)
		}
	}

	if len(matchedStatuses) == 0 {
		return []v1alpha1.ResourceStatus{v1alpha1.UndefinedStatus}, nil
	}

	return matchedStatuses, nil
}

func evaluateMatchers(ctx context.Context, jqRunner execution.Runner, phase *string, conditionsMap map[string]Condition, matchers []v1alpha1.StatusMatcher) (bool, error) {
	for _, matcher := range matchers {
		matched, err := match(ctx, jqRunner, phase, conditionsMap, matcher)
		if err != nil {
			return false, fmt.Errorf("failed to evaluate matcher: %w", err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func match(ctx context.Context, jqRunner execution.Runner, phase *string, conditionsMap map[string]Condition, matcher v1alpha1.StatusMatcher) (bool, error) {
	if matcher.ByPhase != "" {
		if phase == nil || *phase != matcher.ByPhase {
			return false, nil
		}
	}

	for _, expectedCond := range matcher.ByConditions {
		actualCond, found := checkCondition(conditionsMap, expectedCond)
		if !found {
			return false, nil
		}

		expectedValue := ptr.Deref(expectedCond.Status, "")
		if expectedValue != "" && expectedValue != ptr.Deref(actualCond.Status, "") {
			return false, nil
		}

		expectedValue = ptr.Deref(expectedCond.Reason, "")
		if expectedValue != "" && expectedValue != ptr.Deref(actualCond.Reason, "") {
			return false, nil
		}
	}

	if matcher.ByExpression != nil {
		matched, err := matchByExpression(ctx, jqRunner, matcher)
		if err != nil {
			return false, err
		}

		return matched, nil
	}

	return true, nil
}

func matchByExpression(ctx context.Context, jqRunner execution.Runner, matcher v1alpha1.StatusMatcher) (bool, error) {
	// Construct a jq expression that compares the result with the expected value
	comparisonExpr := fmt.Sprintf("(%s) | tostring == \"%s\"", matcher.ByExpression.Expression, matcher.ByExpression.ExpectedResult)

	results, err := jqRunner.Evaluate(ctx, comparisonExpr)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate ByExpression: %w", err)
	}

	if len(results) == 0 {
		return false, nil
	}

	result := results[0]
	if result == nil {
		return false, nil
	}

	// The result must be a boolean from the jq comparison expression
	matched, ok := result.(bool)
	if !ok {
		return false, fmt.Errorf("expression comparison did not return a boolean, got %T", result)
	}

	return matched, nil
}

func checkCondition(conditionsMap map[string]Condition, expectedCond v1alpha1.ExpectedCondition) (Condition, bool) {
	actualCond, found := conditionsMap[expectedCond.Type]
	if !found {
		return Condition{}, false
	}
	return actualCond, true
}
