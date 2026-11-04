// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 NVIDIA Corporation

package resource

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/run-ai/karta/pkg/api/runai/v1alpha1"
	"github.com/run-ai/karta/pkg/jq/execution"
)

// InstanceNotFoundError is returned when a pod's extracted instance ID doesn't match any valid instance IDs
type InstanceNotFoundError string

func (e InstanceNotFoundError) Error() string {
	return string(e)
}

// PodQuerier handles JQ-based querying operations against pods
type PodQuerier struct {
	pod       *corev1.Pod
	evaluator execution.Evaluator
}

func NewPodQuerier(pod *corev1.Pod) *PodQuerier {
	return &PodQuerier{
		pod:       pod,
		evaluator: execution.NewDefaultRunner(pod),
	}
}

func (pq *PodQuerier) GetPodName() string {
	return pq.pod.Name
}

// MatchesComponentType returns true if the pod matches the given component type selector
func (pq *PodQuerier) MatchesComponentType(ctx context.Context, selector *v1alpha1.ComponentTypeSelector) (bool, error) {
	if selector == nil {
		return false, nil
	}

	if selector.Value == nil {
		// Existence check: key should exist and not be nil
		return pq.checkKeyExists(ctx, selector.KeyPath)
	} else {
		// Equality check: key should equal the specified value
		return pq.checkKeyValue(ctx, selector.KeyPath, *selector.Value)
	}
}

// checkKeyExists returns true if the key exists
func (pq *PodQuerier) checkKeyExists(ctx context.Context, keyPath string) (bool, error) {
	results, err := pq.evaluator.Evaluate(ctx, keyPath)
	if err != nil {
		return false, err
	}

	// Key exists if we get any non-nil result
	for _, result := range results {
		if result != nil {
			return true, nil
		}
	}
	return false, nil
}

// checkKeyValue returns true if the key equals the expected value
func (pq *PodQuerier) checkKeyValue(ctx context.Context, keyPath, expectedValue string) (bool, error) {
	serializedValue, err := serialize(expectedValue)
	if err != nil {
		return false, fmt.Errorf("failed to serialize selector value: %w", err)
	}

	query := fmt.Sprintf("%s == %s", keyPath, serializedValue)
	results, err := pq.evaluator.Evaluate(ctx, query)
	if err != nil {
		return false, err
	}

	// Equality check: look for explicit true
	for _, result := range results {
		if result == true {
			return true, nil
		}
	}
	return false, nil
}

// ExtractInstanceId extracts the component instance identifier from the pod using the given ComponentInstanceSelector.
// Returns the instance id as a string, a boolean indicating whether a value was found, and an error.
// When the selector is nil or has an empty IdPath, found is false with no error.
func (pq *PodQuerier) ExtractInstanceId(ctx context.Context, instanceSelector *v1alpha1.ComponentInstanceSelector) (string, bool, error) {
	if instanceSelector == nil || instanceSelector.IdPath == "" {
		return "", false, nil
	}

	value, err := pq.evaluateStringField(ctx, instanceSelector.IdPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to extract instance id from path %q: %w", instanceSelector.IdPath, err)
	}
	return value, true, nil
}

// ExtractReplicaKey extracts the replica identifier from the pod using the given ReplicaSelector.
// Returns the replica key as a string, a boolean indicating whether a value was found, and an error.
// When the selector is nil or has an empty KeyPath, found is false with no error.
func (pq *PodQuerier) ExtractReplicaKey(ctx context.Context, selector *v1alpha1.ReplicaSelector) (string, bool, error) {
	if selector == nil || selector.KeyPath == "" {
		return "", false, nil
	}

	value, err := pq.evaluateStringField(ctx, selector.KeyPath)
	if err != nil {
		return "", false, fmt.Errorf("failed to extract replica key from path %q: %w", selector.KeyPath, err)
	}
	return value, true, nil
}

// ExtractGroupKeys extracts grouping key values from the pod using the provided JQ paths
func (pq *PodQuerier) ExtractGroupKeys(ctx context.Context, keyPaths []string) ([]string, error) {
	if len(keyPaths) == 0 {
		return []string{}, nil
	}

	groupKeys := make([]string, 0, len(keyPaths))

	for _, keyPath := range keyPaths {
		value, err := pq.evaluateStringField(ctx, keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to extract group key from path %q: %w", keyPath, err)
		}

		groupKeys = append(groupKeys, value)
	}

	return groupKeys, nil
}

// PassesFilters returns true if the pod passes all the provided JQ filter expressions
// All filters must pass (AND logic) for the method to return true
func (pq *PodQuerier) PassesFilters(ctx context.Context, filters []string) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}

	for _, filter := range filters {
		result, err := pq.evaluateSingleResult(ctx, filter)
		if err != nil {
			return false, fmt.Errorf("failed to evaluate filter %q: %w", filter, err)
		}

		if result != true {
			return false, nil
		}
	}

	return true, nil
}

// GetMatchingInstanceId checks if the pod matches any of the provided instance ids using the instance selector.
// Returns the matching instance id if found, empty string if no match.
func (pq *PodQuerier) GetMatchingInstanceId(ctx context.Context, instanceSelector *v1alpha1.ComponentInstanceSelector, instanceIds []string) (string, error) {
	if instanceSelector == nil {
		// No instance selector - check if single instance with empty id is expected
		if len(instanceIds) == 1 && instanceIds[0] == "" {
			return "", nil // Match for single instance with empty id
		}
		return "", fmt.Errorf("no instance selector provided but instance ids are not empty")
	}

	podInstanceId, _, err := pq.ExtractInstanceId(ctx, instanceSelector)
	if err != nil {
		return "", err
	}

	// Check if pod's instance id matches any of the existing instance ids
	for _, id := range instanceIds {
		if podInstanceId == id {
			return id, nil
		}
	}

	return "", InstanceNotFoundError(fmt.Sprintf("could not match instance id %q. existing instance ids %v", podInstanceId, instanceIds))
}

// evaluateStringField evaluates a JQ expression and returns the single result as a string.
func (pq *PodQuerier) evaluateStringField(ctx context.Context, jqPath string) (string, error) {
	result, err := pq.evaluateSingleResult(ctx, jqPath)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%v", result), nil
}

// evaluateSingleResult evaluates a JQ expression and validates it returns exactly one non-empty result.
func (pq *PodQuerier) evaluateSingleResult(ctx context.Context, jqPath string) (any, error) {
	results, err := pq.evaluator.Evaluate(ctx, jqPath)
	if err != nil {
		return nil, err
	}
	if err = validateSingleQueryResult(results); err != nil {
		return nil, err
	}
	return results[0], nil
}

func validateSingleQueryResult(results []any) error {
	if len(results) != 1 {
		return fmt.Errorf("expected single query result, got %d", len(results))
	}
	if results[0] == nil || results[0] == "" {
		return fmt.Errorf("query result is empty %v", results[0])
	}
	return nil
}

// serialize serializes a value for use in JQ expressions
func serialize(value string) (string, error) {
	// Use JSON marshaling to properly escape the string for JQ
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}
