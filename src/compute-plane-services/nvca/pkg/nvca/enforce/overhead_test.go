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

package enforce

import (
	"context"
	"errors"
	"testing"

	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	applynodev1 "k8s.io/client-go/applyconfigurations/node/v1"
	"k8s.io/client-go/kubernetes/fake"
	nodev1typed "k8s.io/client-go/kubernetes/typed/node/v1"
	ktest "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	featureflagmock "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag/mock"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/enforce/kata"
)

// createMockFetcher creates a mock feature flag fetcher with specified flags enabled
func createMockFetcher(infraOverheadEnabled, kataEnabled bool) *featureflagmock.Fetcher {
	fetcher := &featureflagmock.Fetcher{}

	var enabledFFs []*featureflag.FeatureFlag
	var enabledAttrs []*featureflag.Attribute

	if infraOverheadEnabled {
		enabledFFs = append(enabledFFs, featureflag.InfraResourceOverhead)
	}

	if kataEnabled {
		enabledAttrs = append(enabledAttrs, featureflag.AttrKataRuntimeIsolation)
	}

	fetcher.SetFeatureFlags(enabledFFs...)
	fetcher.SetAttributes(enabledAttrs...)

	return fetcher
}

func TestNewInfraOverheadGetter(t *testing.T) {
	tests := []struct {
		name                         string
		infraResourceOverheadEnabled bool
		kataEnabled                  bool
		runtimeClass                 *nodev1.RuntimeClass
		runtimeClassGetError         error
		expectError                  bool
		expectEmptyResult            bool
		expectKataOverhead           bool
	}{
		{
			name:                         "infra resource overhead disabled - should return empty result",
			infraResourceOverheadEnabled: false,
			kataEnabled:                  false,
			expectEmptyResult:            true,
		},
		{
			name:                         "infra enabled, kata disabled - should return default overhead only",
			infraResourceOverheadEnabled: true,
			kataEnabled:                  false,
			expectEmptyResult:            false,
		},
		{
			name:                         "both enabled - runtime class found with overhead",
			infraResourceOverheadEnabled: true,
			kataEnabled:                  true,
			runtimeClass: &nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: kata.RuntimeClassNameGPU,
				},
				Overhead: &nodev1.Overhead{
					PodFixed: corev1.ResourceList{
						corev1.ResourceCPU:              resource.MustParse("500m"),
						corev1.ResourceMemory:           resource.MustParse("512Mi"),
						corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
					},
				},
			},
			expectKataOverhead: true,
		},
		{
			name:                         "both enabled - runtime class found with no overhead",
			infraResourceOverheadEnabled: true,
			kataEnabled:                  true,
			runtimeClass: &nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: kata.RuntimeClassNameGPU,
				},
				Overhead: &nodev1.Overhead{
					PodFixed: corev1.ResourceList{},
				},
			},
			expectKataOverhead: false,
		},
		{
			name:                         "both enabled - runtime class not found",
			infraResourceOverheadEnabled: true,
			kataEnabled:                  true,
			runtimeClassGetError:         errors.New("not found"),
			expectError:                  true,
		},
		{
			name:                         "both enabled - runtime class with partial overhead",
			infraResourceOverheadEnabled: true,
			kataEnabled:                  true,
			runtimeClass: &nodev1.RuntimeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: kata.RuntimeClassNameGPU,
				},
				Overhead: &nodev1.Overhead{
					PodFixed: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("200m"),
						// Missing memory and ephemeral storage
					},
				},
			},
			expectKataOverhead: true,
		},
		{
			name:                         "infra enabled, kata enabled but no runtime class - should return error",
			infraResourceOverheadEnabled: true,
			kataEnabled:                  true,
			runtimeClassGetError:         errors.New("runtime class not found"),
			expectError:                  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup fake Kubernetes client
			var objects []runtime.Object
			if tt.runtimeClass != nil {
				objects = append(objects, tt.runtimeClass)
			}
			fakeClient := fake.NewSimpleClientset(objects...)

			// Mock error for runtime class get if needed
			if tt.runtimeClassGetError != nil {
				fakeClient.PrependReactor("get", "runtimeclasses", func(action ktest.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, tt.runtimeClassGetError
				})
			}

			// Create mock feature flag fetcher
			fff := createMockFetcher(tt.infraResourceOverheadEnabled, tt.kataEnabled)

			// Create the overhead getter
			overheadGetter := NewInfraOverheadGetter(fff, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
				return fakeClient.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
			})

			// Call the function
			ctx := context.Background()
			result, err := overheadGetter.GetInfraOverhead(ctx)

			// Check error expectations
			if tt.expectError {
				if err == nil {
					t.Error("expected an error but got none")
				}
				return
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			// Check empty result expectations
			if tt.expectEmptyResult {
				if len(result) != 0 {
					t.Errorf("expected empty result, got %v", result)
				}
				return
			}

			// Verify that we get a non-empty result when expected
			if !tt.expectEmptyResult && len(result) == 0 {
				t.Error("expected non-empty resource list")
			}

			// Verify that runtime class was called when kata is enabled
			actions := fakeClient.Actions()
			if tt.kataEnabled && tt.infraResourceOverheadEnabled {
				found := false
				for _, action := range actions {
					if action.GetVerb() == "get" && action.GetResource().Resource == "runtimeclasses" {
						getAction := action.(ktest.GetAction)
						if getAction.GetName() == kata.RuntimeClassNameGPU {
							found = true
							break
						}
					}
				}
				if !found && tt.runtimeClassGetError == nil {
					t.Error("expected runtime class get call when kata is enabled")
				}
			} else if !tt.kataEnabled || !tt.infraResourceOverheadEnabled {
				// When kata is disabled or infra overhead is disabled, no runtime class calls should be made
				for _, action := range actions {
					if action.GetVerb() == "get" && action.GetResource().Resource == "runtimeclasses" {
						t.Error("unexpected runtime class get call when kata is disabled or infra overhead is disabled")
					}
				}
			}

			// For tests with kata overhead, get a second result without kata to compare
			if tt.expectKataOverhead && tt.runtimeClass != nil {
				// Create a getter without kata enabled to compare
				fffNoKata := createMockFetcher(true, false)
				overheadGetterNoKata := NewInfraOverheadGetter(fffNoKata, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
					return fakeClient.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
				})
				resultNoKata, errNoKata := overheadGetterNoKata.GetInfraOverhead(ctx)

				if errNoKata != nil {
					t.Errorf("unexpected error in no-kata test: %v", errNoKata)
					return
				}

				// Verify that kata adds overhead for each resource in the runtime class
				for resourceName, kataAmount := range tt.runtimeClass.Overhead.PodFixed {
					resultAmount, resultExists := result[resourceName]
					noKataAmount, noKataExists := resultNoKata[resourceName]

					if !resultExists {
						t.Errorf("expected resource %s to exist in result with kata overhead", resourceName)
						continue
					}

					if !noKataExists {
						// If the resource doesn't exist without kata, the result should be at least the kata overhead
						if resultAmount.Cmp(kataAmount) < 0 {
							t.Errorf("expected %s with kata to be at least %s, got %s", resourceName, kataAmount.String(), resultAmount.String())
						}
					} else {
						// If the resource exists without kata, the difference should be at least the kata overhead
						actualDiff := resultAmount.DeepCopy()
						actualDiff.Sub(noKataAmount)
						if actualDiff.Cmp(kataAmount) < 0 {
							t.Errorf("expected %s kata overhead to be at least %s, got difference of %s",
								resourceName, kataAmount.String(), actualDiff.String())
						}
					}
				}
			}
		})
	}
}

func TestNewInfraOverheadGetter_InterfaceCompliance(t *testing.T) {
	// Test that the returned function implements the InfraOverheadGetter interface
	fff := createMockFetcher(false, false)
	fakeClient := fake.NewSimpleClientset()

	overheadGetter := NewInfraOverheadGetter(fff, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
		return fakeClient.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	})

	// This should compile - testing interface compliance
	var _ InfraOverheadGetter = overheadGetter

	// Test that we can call the method
	ctx := context.Background()
	result, err := overheadGetter.GetInfraOverhead(ctx)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if result == nil {
		t.Error("expected non-nil resource list")
	}
}

func TestNewInfraOverheadGetter_ContextCancellation(t *testing.T) {
	// Test behavior when context is cancelled
	fff := createMockFetcher(true, true)
	fakeClient := fake.NewSimpleClientset()

	// Add a reactor that simulates context cancellation
	fakeClient.PrependReactor("get", "runtimeclasses", func(action ktest.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, context.Canceled
	})

	overheadGetter := NewInfraOverheadGetter(fff, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
		return fakeClient.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Should return an error when the kata runtime class fetch fails
	_, err := overheadGetter.GetInfraOverhead(ctx)

	if err == nil {
		t.Error("expected error due to context cancellation")
	}

	if err != context.Canceled {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
}

func TestInfraOverheadGetterFunc_Implementation(t *testing.T) {
	// Test that InfraOverheadGetterFunc correctly implements the interface
	called := false
	expectedResult := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"),
	}
	expectedError := errors.New("test error")

	getter := InfraOverheadGetterFunc(func(ctx context.Context) (corev1.ResourceList, error) {
		called = true
		if ctx == nil {
			t.Error("context should not be nil")
		}
		return expectedResult, expectedError
	})

	ctx := context.Background()
	result, err := getter.GetInfraOverhead(ctx)

	if !called {
		t.Error("function was not called")
	}

	if err != expectedError {
		t.Errorf("expected error %v, got %v", expectedError, err)
	}

	if !result.Cpu().Equal(*expectedResult.Cpu()) {
		t.Errorf("expected result %v, got %v", expectedResult, result)
	}
}

// Test with different runtime class getter implementations
type mockRuntimeClassGetter struct {
	runtimeClass *nodev1.RuntimeClass
	err          error
}

func (m *mockRuntimeClassGetter) RuntimeClasses() nodev1typed.RuntimeClassInterface {
	return &mockRuntimeClassInterface{
		runtimeClass: m.runtimeClass,
		err:          m.err,
	}
}

type mockRuntimeClassInterface struct {
	runtimeClass *nodev1.RuntimeClass
	err          error
}

func (m *mockRuntimeClassInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*nodev1.RuntimeClass, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.runtimeClass != nil && m.runtimeClass.Name == name {
		return m.runtimeClass, nil
	}
	return nil, errors.New("not found")
}

// Implement other required methods (not used in our test)
func (m *mockRuntimeClassInterface) Create(ctx context.Context, runtimeClass *nodev1.RuntimeClass, opts metav1.CreateOptions) (*nodev1.RuntimeClass, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) Update(ctx context.Context, runtimeClass *nodev1.RuntimeClass, opts metav1.UpdateOptions) (*nodev1.RuntimeClass, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) List(ctx context.Context, opts metav1.ListOptions) (*nodev1.RuntimeClassList, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *nodev1.RuntimeClass, err error) {
	return nil, errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) Apply(ctx context.Context, runtimeClass *applynodev1.RuntimeClassApplyConfiguration, opts metav1.ApplyOptions) (result *nodev1.RuntimeClass, err error) {
	return nil, errors.New("not implemented")
}
func (m *mockRuntimeClassInterface) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	return errors.New("not implemented")
}

func TestNewInfraOverheadGetter_CustomMock(t *testing.T) {
	// Test with custom mock to ensure we're testing the right runtime class name
	runtimeClass := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: kata.RuntimeClassNameGPU,
		},
		Overhead: &nodev1.Overhead{
			PodFixed: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}

	fff := createMockFetcher(true, true)
	mockGetter := &mockRuntimeClassGetter{runtimeClass: runtimeClass}

	overheadGetter := NewInfraOverheadGetter(fff, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
		return mockGetter.RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	})

	ctx := context.Background()
	result, err := overheadGetter.GetInfraOverhead(ctx)

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify that we get a non-empty result containing resources
	if len(result) == 0 {
		t.Error("expected non-empty resource list")
	}

	// Verify that the result includes both CPU and memory resources
	if _, exists := result[corev1.ResourceCPU]; !exists {
		t.Error("expected CPU resource to be present")
	}

	if _, exists := result[corev1.ResourceMemory]; !exists {
		t.Error("expected Memory resource to be present")
	}

	// Compare with result when kata is disabled to verify kata overhead is added
	fffNoKata := createMockFetcher(true, false)
	overheadGetterNoKata := NewInfraOverheadGetter(fffNoKata, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
		return mockGetter.RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	})
	resultNoKata, errNoKata := overheadGetterNoKata.GetInfraOverhead(ctx)

	if errNoKata != nil {
		t.Errorf("unexpected error in no-kata test: %v", errNoKata)
	}

	// Verify that kata overhead is added
	cpuWithKata := result[corev1.ResourceCPU]
	cpuNoKata := resultNoKata[corev1.ResourceCPU]
	expectedCPUDiff := resource.MustParse("1")

	actualCPUDiff := cpuWithKata.DeepCopy()
	actualCPUDiff.Sub(cpuNoKata)

	if actualCPUDiff.Cmp(expectedCPUDiff) < 0 {
		t.Errorf("expected CPU difference of at least %s, got %s", expectedCPUDiff.String(), actualCPUDiff.String())
	}

	memoryWithKata := result[corev1.ResourceMemory]
	memoryNoKata := resultNoKata[corev1.ResourceMemory]
	expectedMemoryDiff := resource.MustParse("1Gi")

	actualMemoryDiff := memoryWithKata.DeepCopy()
	actualMemoryDiff.Sub(memoryNoKata)

	if actualMemoryDiff.Cmp(expectedMemoryDiff) < 0 {
		t.Errorf("expected Memory difference of at least %s, got %s", expectedMemoryDiff.String(), actualMemoryDiff.String())
	}
}

func TestNewInfraOverheadGetter_ErrorPropagation(t *testing.T) {
	// Test that errors from runtime class retrieval are properly propagated
	expectedError := errors.New("kubernetes API error")

	fff := createMockFetcher(true, true)
	mockGetter := &mockRuntimeClassGetter{err: expectedError}

	overheadGetter := NewInfraOverheadGetter(fff, nvcaconfig.Config{}, func(ctx context.Context, name string) (*nodev1.RuntimeClass, error) {
		return mockGetter.RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	})

	ctx := context.Background()
	_, err := overheadGetter.GetInfraOverhead(ctx)

	if err == nil {
		t.Error("expected error to be propagated")
	}

	if err != expectedError {
		t.Errorf("expected error %v, got %v", expectedError, err)
	}
}
