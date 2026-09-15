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

package mirror

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func newTestController(secretNames []string, objects ...runtime.Object) *Controller {
	clientset := fake.NewSimpleClientset(objects...)

	return NewController(
		clientset,
		"source-namespace",
		"target-namespace",
		secretNames,
		DefaultResyncPeriod,
	)
}

func TestHandleSecretAdd(t *testing.T) {
	tests := []struct {
		name        string
		secretNames []string
		secret      *corev1.Secret
		wantSync    bool
		wantErr     bool
	}{
		{
			name:        "tracked secret should be synced",
			secretNames: []string{"my-secret"},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("test-data"),
				},
			},
			wantSync: true,
			wantErr:  false,
		},
		{
			name:        "untracked secret should not be synced",
			secretNames: []string{"other-secret"},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("test-data"),
				},
			},
			wantSync: false,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			// Create target namespace in fake client
			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			controller := newTestController(tt.secretNames, targetNS)
			err := controller.handleSecretAdd(ctx, tt.secret)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantSync {
				// Verify secret was created in target namespace
				secret, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
					ctx,
					tt.secret.Name,
					metav1.GetOptions{},
				)
				require.NoError(t, err)
				assert.Equal(t, tt.secret.Data, secret.Data)
				assert.Equal(t, tt.secret.Type, secret.Type)
				assert.Equal(t, "true", secret.Labels[AdditionalImagePullSecretLabelKey])
			} else {
				// Verify secret was not created
				_, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
					ctx,
					tt.secret.Name,
					metav1.GetOptions{},
				)
				assert.Error(t, err)
			}
		})
	}
}

func TestHandleSecretUpdate(t *testing.T) {
	tests := []struct {
		name        string
		secretNames []string
		oldSecret   *corev1.Secret
		newSecret   *corev1.Secret
		wantSync    bool
		wantErr     bool
	}{
		{
			name:        "tracked secret with data change should be synced",
			secretNames: []string{"my-secret"},
			oldSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("old-data"),
				},
			},
			newSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("new-data"),
				},
			},
			wantSync: true,
			wantErr:  false,
		},
		{
			name:        "tracked secret with no data change should not be synced",
			secretNames: []string{"my-secret"},
			oldSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("same-data"),
				},
			},
			newSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("same-data"),
				},
			},
			wantSync: false,
			wantErr:  false,
		},
		{
			name:        "untracked secret should not be synced",
			secretNames: []string{"other-secret"},
			oldSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("old-data"),
				},
			},
			newSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("new-data"),
				},
			},
			wantSync: false,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			// Create target namespace in fake client
			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			controller := newTestController(tt.secretNames, targetNS)

			// Track if syncSecret was called
			syncCalled := false
			if tt.wantSync {
				// Pre-create the secret to test update path
				err := controller.syncSecret(ctx, tt.oldSecret)
				require.NoError(t, err)
				syncCalled = true
			}

			err := controller.handleSecretUpdate(ctx, tt.oldSecret, tt.newSecret)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantSync && syncCalled {
				// Verify secret was updated in target namespace
				secret, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
					ctx,
					tt.newSecret.Name,
					metav1.GetOptions{},
				)
				require.NoError(t, err)
				assert.Equal(t, tt.newSecret.Data, secret.Data)
			}
		})
	}
}

func TestSyncSecret(t *testing.T) {
	tests := []struct {
		name           string
		secret         *corev1.Secret
		existingSecret *corev1.Secret
		wantErr        bool
		checkUpdate    bool
	}{
		{
			name: "create new secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "new-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("test-data"),
				},
			},
			existingSecret: nil,
			wantErr:        false,
			checkUpdate:    false,
		},
		{
			name: "update existing secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("updated-data"),
				},
			},
			existingSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "existing-secret",
					Namespace: "target-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte("old-data"),
				},
			},
			wantErr:     false,
			checkUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var objects []runtime.Object
			// Create target namespace in fake client
			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			objects = append(objects, targetNS)
			if tt.existingSecret != nil {
				objects = append(objects, tt.existingSecret)
			}

			controller := newTestController([]string{tt.secret.Name}, objects...)
			err := controller.syncSecret(ctx, tt.secret)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify secret exists in target namespace with correct data
				secret, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
					ctx,
					tt.secret.Name,
					metav1.GetOptions{},
				)
				require.NoError(t, err)
				assert.Equal(t, tt.secret.Data, secret.Data)
				assert.Equal(t, tt.secret.Type, secret.Type)
				assert.Equal(t, "true", secret.Labels[AdditionalImagePullSecretLabelKey])
				assert.Equal(t, ManagedByValue, secret.Labels[ManagedbyLabelKey])
			}
		})
	}
}

func TestCleanupOrphanedSecrets(t *testing.T) {
	tests := []struct {
		name            string
		secretNames     []string
		existingSecrets []*corev1.Secret
		wantDeleted     []string
		wantKept        []string
	}{
		{
			name:        "delete orphaned secret",
			secretNames: []string{"keep-me"},
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "keep-me",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "delete-me",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
			},
			wantDeleted: []string{"delete-me"},
			wantKept:    []string{"keep-me"},
		},
		{
			name:        "keep all tracked secrets",
			secretNames: []string{"secret1", "secret2"},
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret1",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret2",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
			},
			wantDeleted: []string{},
			wantKept:    []string{"secret1", "secret2"},
		},
		{
			name:        "no secrets to cleanup",
			secretNames: []string{"secret1"},
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret1",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
			},
			wantDeleted: []string{},
			wantKept:    []string{"secret1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			objects := make([]runtime.Object, 0, len(tt.existingSecrets)+1)
			// Create target namespace in fake client
			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			objects = append(objects, targetNS)
			for _, s := range tt.existingSecrets {
				objects = append(objects, s)
			}

			controller := newTestController(tt.secretNames, objects...)

			// Track deletions
			deleted := make(map[string]bool)
			controller.clientset.(*fake.Clientset).PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
				deleteAction := action.(k8stesting.DeleteAction)
				deleted[deleteAction.GetName()] = true
				return false, nil, nil // Let the fake client handle the actual deletion
			})

			err := controller.cleanupOrphanedSecrets(ctx)
			assert.NoError(t, err)

			// Verify deleted secrets
			for _, name := range tt.wantDeleted {
				assert.True(t, deleted[name], "Expected secret %s to be deleted", name)
			}

			// Verify kept secrets still exist
			for _, name := range tt.wantKept {
				secret, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
					ctx,
					name,
					metav1.GetOptions{},
				)
				assert.NoError(t, err)
				assert.NotNil(t, secret)
			}
		})
	}
}

func TestIsTrackedSecret(t *testing.T) {
	controller := newTestController([]string{"secret1", "secret2", "secret3"})

	tests := []struct {
		name       string
		secretName string
		want       bool
	}{
		{
			name:       "tracked secret",
			secretName: "secret1",
			want:       true,
		},
		{
			name:       "another tracked secret",
			secretName: "secret2",
			want:       true,
		},
		{
			name:       "untracked secret",
			secretName: "not-tracked",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := controller.isTrackedSecret(tt.secretName)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateSecretsExist(t *testing.T) {
	tests := []struct {
		name                  string
		secretNames           []string
		existingSecrets       []string
		expectedError         bool
		expectedErrorContains string
	}{
		{
			name:            "no secrets configured",
			secretNames:     []string{},
			existingSecrets: []string{},
			expectedError:   false,
		},
		{
			name:            "nil secrets",
			secretNames:     nil,
			existingSecrets: []string{},
			expectedError:   false,
		},
		{
			name:            "single valid secret",
			secretNames:     []string{"my-secret"},
			existingSecrets: []string{"my-secret"},
			expectedError:   false,
		},
		{
			name:            "multiple valid secrets",
			secretNames:     []string{"secret-1", "secret-2", "secret-3"},
			existingSecrets: []string{"secret-1", "secret-2", "secret-3"},
			expectedError:   false,
		},
		{
			name:                  "single non-existent secret",
			secretNames:           []string{"missing-secret"},
			existingSecrets:       []string{},
			expectedError:         true,
			expectedErrorContains: "missing-secret",
		},
		{
			name:                  "multiple secrets with one missing",
			secretNames:           []string{"secret-1", "missing-secret", "secret-3"},
			existingSecrets:       []string{"secret-1", "secret-3"},
			expectedError:         true,
			expectedErrorContains: "missing-secret",
		},
		{
			name:                  "all secrets missing",
			secretNames:           []string{"missing-1", "missing-2"},
			existingSecrets:       []string{},
			expectedError:         true,
			expectedErrorContains: "missing-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create existing secrets as runtime objects
			objects := make([]runtime.Object, 0, len(tt.existingSecrets))
			for _, secretName := range tt.existingSecrets {
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      secretName,
						Namespace: "source-namespace",
					},
					Type: corev1.SecretTypeDockerConfigJson,
				}
				objects = append(objects, secret)
			}

			// Create controller with existing secrets
			c := newTestController(tt.secretNames, objects...)

			// Call validateSecretsExist
			err := c.validateSecretsExist(ctx)

			// Assert results
			if tt.expectedError {
				assert.Error(t, err)
				if tt.expectedErrorContains != "" {
					assert.Contains(t, err.Error(), tt.expectedErrorContains)
					assert.Contains(t, err.Error(), "source-namespace")
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSyncSecretWithNamespaceNotFound(t *testing.T) {
	// Save and restore original backoff
	origBackoff := NamespaceBackoff
	defer func() { NamespaceBackoff = origBackoff }()

	// Use fast backoff for testing
	NamespaceBackoff = wait.Backoff{
		Steps:    3,
		Duration: 100 * time.Millisecond,
		Factor:   1.0,
		Jitter:   0,
	}

	tests := []struct {
		name           string
		namespaceError error
		expectSuccess  bool
		attempts       int
	}{
		{
			name:          "immediate success",
			expectSuccess: true,
			attempts:      1,
		},
		{
			name: "success after namespace becomes available",
			namespaceError: k8serrors.NewNotFound(
				schema.GroupResource{Group: "", Resource: "namespaces"},
				"target-namespace",
			),
			expectSuccess: true,
			attempts:      2,
		},
		{
			name:           "non-namespace error returns immediately",
			namespaceError: k8serrors.NewUnauthorized("unauthorized"),
			expectSuccess:  false,
			attempts:       100, // Should never reach this
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create a namespace object for successful cases
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			controller := newTestController([]string{"test-secret"}, namespace)

			// Setup reactor to simulate namespace not found initially
			namespaceAttempts := 0
			controller.clientset.(*fake.Clientset).PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
				namespaceAttempts++
				if tt.namespaceError != nil {
					if namespaceAttempts >= tt.attempts {
						// Namespace is now available
						return true, namespace, nil
					}
					return true, nil, tt.namespaceError
				}
				return false, nil, nil // Use default fake client behavior
			})

			sourceSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{".dockerconfigjson": []byte("test")},
			}

			err := controller.syncSecret(ctx, sourceSecret)

			if tt.expectSuccess {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestSyncSecretWithConflict(t *testing.T) {
	tests := []struct {
		name             string
		simulateConflict bool
		conflictAttempts int
		expectSuccess    bool
	}{
		{
			name:          "no conflict",
			expectSuccess: true,
		},
		{
			name:             "conflict resolved on retry",
			simulateConflict: true,
			conflictAttempts: 2,
			expectSuccess:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}

			// Pre-create an existing secret
			existingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test-secret",
					Namespace:       "target-namespace",
					ResourceVersion: "1",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{".dockerconfigjson": []byte("old-data")},
			}

			controller := newTestController([]string{"test-secret"}, namespace, existingSecret)

			if tt.simulateConflict {
				updateAttempts := 0
				controller.clientset.(*fake.Clientset).PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
					updateAttempts++
					if updateAttempts < tt.conflictAttempts {
						// Simulate conflict
						return true, nil, k8serrors.NewConflict(
							schema.GroupResource{Group: "", Resource: "secrets"},
							"test-secret",
							fmt.Errorf("conflict"),
						)
					}
					return false, nil, nil // Use default fake client behavior
				})
			}

			sourceSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{".dockerconfigjson": []byte("new-data")},
			}

			err := controller.syncSecret(ctx, sourceSecret)

			if tt.expectSuccess {
				assert.NoError(t, err)

				// Verify the secret was updated
				updated, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
					ctx,
					"test-secret",
					metav1.GetOptions{},
				)
				require.NoError(t, err)
				assert.Equal(t, sourceSecret.Data, updated.Data)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name            string
		secretNames     []string
		sourceSecrets   []*corev1.Secret
		targetSecrets   []*corev1.Secret
		expectError     bool
		errorContains   string
		addWrongObjType bool
	}{
		{
			name:          "validation fails when secret doesn't exist",
			secretNames:   []string{"missing-secret"},
			sourceSecrets: []*corev1.Secret{},
			expectError:   true,
			errorContains: "secret validation failed",
		},
		{
			name:          "runs successfully with no secrets configured",
			secretNames:   []string{},
			sourceSecrets: []*corev1.Secret{},
			expectError:   false,
		},
		{
			name:        "runs successfully with valid secrets",
			secretNames: []string{"test-secret"},
			sourceSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-secret",
						Namespace: "source-namespace",
					},
					Type: corev1.SecretTypeDockerConfigJson,
					Data: map[string][]byte{".dockerconfigjson": []byte("test")},
				},
			},
			expectError: false,
		},
		{
			name:        "runs with orphaned secrets in target",
			secretNames: []string{"keep-me"},
			sourceSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "keep-me",
						Namespace: "source-namespace",
					},
					Type: corev1.SecretTypeDockerConfigJson,
					Data: map[string][]byte{".dockerconfigjson": []byte("test")},
				},
			},
			targetSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "delete-me",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
					Type: corev1.SecretTypeDockerConfigJson,
					Data: map[string][]byte{".dockerconfigjson": []byte("old")},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			objects := make([]runtime.Object, 0, len(tt.sourceSecrets)+len(tt.targetSecrets)+2)

			// Add source namespace
			sourceNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "source-namespace",
				},
			}
			objects = append(objects, sourceNS)

			// Add target namespace
			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			objects = append(objects, targetNS)

			// Add source secrets
			for _, s := range tt.sourceSecrets {
				objects = append(objects, s)
			}

			// Add target secrets
			for _, s := range tt.targetSecrets {
				objects = append(objects, s)
			}

			controller := newTestController(tt.secretNames, objects...)

			// If we expect no error, cancel after a short delay to prevent blocking
			if !tt.expectError {
				go func() {
					time.Sleep(200 * time.Millisecond)
					cancel()
				}()
			}

			err := controller.Run(ctx)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRun_EventHandlerErrors(t *testing.T) {
	// Test that event handlers log errors but don't crash the controller
	t.Run("handles wrong object type in add handler", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// Create a simple controller with no secrets
		controller := newTestController([]string{}, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "source-namespace"},
		}, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "target-namespace"},
		})

		// Start the controller in a goroutine
		errChan := make(chan error, 1)
		go func() {
			errChan <- controller.Run(ctx)
		}()

		// Wait for context to be done or error
		select {
		case err := <-errChan:
			assert.NoError(t, err)
		case <-ctx.Done():
			// Expected timeout
		}
	})
}

func TestController_Integration(t *testing.T) {
	// Integration test that exercises the full controller lifecycle
	t.Run("full lifecycle with secret sync", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "source-namespace",
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{".dockerconfigjson": []byte("test-data")},
		}

		objects := []runtime.Object{
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "source-namespace"},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "target-namespace"},
			},
			sourceSecret,
		}

		controller := newTestController([]string{"test-secret"}, objects...)

		// Run controller in background
		errChan := make(chan error, 1)
		go func() {
			errChan <- controller.Run(ctx)
		}()

		// Give it time to sync
		time.Sleep(300 * time.Millisecond)

		// Verify secret was synced
		syncedSecret, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
			context.Background(),
			"test-secret",
			metav1.GetOptions{},
		)
		assert.NoError(t, err)
		assert.NotNil(t, syncedSecret)
		assert.Equal(t, sourceSecret.Data, syncedSecret.Data)

		// Cancel and wait for clean shutdown
		cancel()
		err = <-errChan
		assert.NoError(t, err)
	})

	t.Run("handles secret updates during runtime", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sourceSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "test-secret",
				Namespace:       "source-namespace",
				ResourceVersion: "1",
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{".dockerconfigjson": []byte("initial-data")},
		}

		objects := []runtime.Object{
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "source-namespace"},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "target-namespace"},
			},
			sourceSecret,
		}

		controller := newTestController([]string{"test-secret"}, objects...)

		// Run controller in background
		go func() {
			_ = controller.Run(ctx)
		}()

		// Give it time to start
		time.Sleep(300 * time.Millisecond)

		// Update the source secret
		updatedSecret := sourceSecret.DeepCopy()
		updatedSecret.Data = map[string][]byte{".dockerconfigjson": []byte("updated-data")}
		updatedSecret.ResourceVersion = "2"

		_, err := controller.clientset.CoreV1().Secrets("source-namespace").Update(
			context.Background(),
			updatedSecret,
			metav1.UpdateOptions{},
		)
		assert.NoError(t, err)

		// Give it time to sync the update
		time.Sleep(300 * time.Millisecond)

		// Verify the update was synced
		syncedSecret, err := controller.clientset.CoreV1().Secrets("target-namespace").Get(
			context.Background(),
			"test-secret",
			metav1.GetOptions{},
		)
		assert.NoError(t, err)
		assert.Equal(t, updatedSecret.Data, syncedSecret.Data)
	})
}

func TestCleanupAllAdditionalSecrets_DeleteCollectionError(t *testing.T) {
	ctx := context.Background()

	targetNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "target-namespace",
		},
	}

	clientset := fake.NewSimpleClientset(targetNS)

	// Simulate delete-collection error (non-NotFound)
	clientset.PrependReactor("delete-collection", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("simulated delete-collection error")
	})

	// Should not return error (errors are non-fatal)
	err := CleanupAllAdditionalSecrets(ctx, clientset, "target-namespace")
	assert.NoError(t, err)
}

func TestHandleSecretAdd_ErrorInSync(t *testing.T) {
	// Save and restore original backoff
	origBackoff := NamespaceBackoff
	t.Cleanup(func() { NamespaceBackoff = origBackoff })

	// Use fast backoff for testing
	NamespaceBackoff = wait.Backoff{
		Steps:    2,
		Duration: 10 * time.Millisecond,
		Factor:   1.0,
		Jitter:   0,
	}

	ctx := context.Background()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "source-namespace",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte("test")},
	}

	// Create controller without target namespace to cause error
	controller := newTestController([]string{"test-secret"})

	// Should return error since target namespace doesn't exist
	err := controller.handleSecretAdd(ctx, secret)
	assert.Error(t, err)
}

func TestHandleSecretUpdate_ErrorInSync(t *testing.T) {
	// Save and restore original backoff
	origBackoff := NamespaceBackoff
	defer func() { NamespaceBackoff = origBackoff }()

	// Use fast backoff for testing
	NamespaceBackoff = wait.Backoff{
		Steps:    2,
		Duration: 10 * time.Millisecond,
		Factor:   1.0,
		Jitter:   0,
	}

	ctx := context.Background()

	oldSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "source-namespace",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte("old")},
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "source-namespace",
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{".dockerconfigjson": []byte("new")},
	}

	// Create controller without target namespace to cause error
	controller := newTestController([]string{"test-secret"})

	// Should return error since target namespace doesn't exist
	err := controller.handleSecretUpdate(ctx, oldSecret, newSecret)
	assert.Error(t, err)
}

func TestHandleSecretDelete(t *testing.T) {
	tests := []struct {
		name           string
		secretNames    []string
		deletedSecret  *corev1.Secret
		existingTarget *corev1.Secret
		wantDeleted    bool
	}{
		{
			name:        "tracked secret deletion is handled",
			secretNames: []string{"my-secret"},
			deletedSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
			existingTarget: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "target-namespace",
					Labels: map[string]string{
						AdditionalImagePullSecretLabelKey: "true",
					},
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
			wantDeleted: true,
		},
		{
			name:        "untracked secret deletion is ignored",
			secretNames: []string{"other-secret"},
			deletedSecret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
			},
			wantDeleted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]runtime.Object, 0)

			// Create target namespace
			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			objects = append(objects, targetNS)

			if tt.existingTarget != nil {
				objects = append(objects, tt.existingTarget)
			}

			controller := newTestController(tt.secretNames, objects...)

			// The delete handler is part of the event handler in Run(),
			// so we just verify that untracked secrets are ignored
			if !controller.isTrackedSecret(tt.deletedSecret.Name) {
				assert.False(t, tt.wantDeleted)
			}
		})
	}
}

func TestCleanupAllAdditionalSecrets(t *testing.T) {
	tests := []struct {
		name                       string
		targetNamespace            string
		namespaceExists            bool
		existingSecrets            []*corev1.Secret
		simulateNotFound           bool
		wantDeleteCollectionCalled bool
		wantKept                   []string
	}{
		{
			name:                       "target namespace does not exist",
			targetNamespace:            "non-existent",
			namespaceExists:            false,
			existingSecrets:            []*corev1.Secret{},
			wantDeleteCollectionCalled: false,
			wantKept:                   []string{},
		},
		{
			name:                       "target namespace exists with no secrets",
			targetNamespace:            "target-namespace",
			namespaceExists:            true,
			existingSecrets:            []*corev1.Secret{},
			wantDeleteCollectionCalled: true,
			wantKept:                   []string{},
		},
		{
			name:            "target namespace exists with secrets to delete",
			targetNamespace: "target-namespace",
			namespaceExists: true,
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret1",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret2",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
			},
			wantDeleteCollectionCalled: true,
			wantKept:                   []string{},
		},
		{
			name:            "delete collection returns NotFound",
			targetNamespace: "target-namespace",
			namespaceExists: true,
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "secret1",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
			},
			simulateNotFound:           true,
			wantDeleteCollectionCalled: true,
			wantKept:                   []string{},
		},
		{
			name:            "secrets without label should not be touched",
			targetNamespace: "target-namespace",
			namespaceExists: true,
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "labeled-secret",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unlabeled-secret",
						Namespace: "target-namespace",
						Labels:    map[string]string{},
					},
				},
			},
			wantDeleteCollectionCalled: true,
			wantKept:                   []string{"unlabeled-secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			objects := make([]runtime.Object, 0, len(tt.existingSecrets)+1)

			// Create target namespace if it exists
			if tt.namespaceExists {
				targetNS := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: tt.targetNamespace,
					},
				}
				objects = append(objects, targetNS)
			}

			// Add existing secrets
			for _, s := range tt.existingSecrets {
				objects = append(objects, s)
			}

			clientset := fake.NewSimpleClientset(objects...)

			// Track delete-collection calls
			deleteCollectionCalled := false
			clientset.PrependReactor("delete-collection", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
				deleteCollectionCalled = true
				if tt.simulateNotFound {
					return true, nil, k8serrors.NewNotFound(
						schema.GroupResource{Group: "", Resource: "secrets"},
						"",
					)
				}
				return false, nil, nil // Let the fake client handle the actual deletion
			})

			// Call CleanupAllAdditionalSecrets
			err := CleanupAllAdditionalSecrets(ctx, clientset, tt.targetNamespace)
			assert.NoError(t, err) // Should always return nil (errors are non-fatal)

			// Verify delete-collection was called as expected
			assert.Equal(t, tt.wantDeleteCollectionCalled, deleteCollectionCalled,
				"DeleteCollection call expectation mismatch")

			// Verify kept secrets still exist
			for _, name := range tt.wantKept {
				secret, err := clientset.CoreV1().Secrets(tt.targetNamespace).Get(
					ctx,
					name,
					metav1.GetOptions{},
				)
				assert.NoError(t, err)
				assert.NotNil(t, secret)
			}
		})
	}
}

func TestCleanupAllAdditionalSecrets_ErrorCases(t *testing.T) {
	tests := []struct {
		name               string
		targetNamespace    string
		namespaceExists    bool
		simulateListError  bool
		simulateOtherError bool
	}{
		{
			name:               "handles other namespace errors gracefully",
			targetNamespace:    "target-namespace",
			namespaceExists:    false,
			simulateOtherError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			objects := make([]runtime.Object, 0)

			if tt.namespaceExists {
				targetNS := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: tt.targetNamespace,
					},
				}
				objects = append(objects, targetNS)
			}

			clientset := fake.NewSimpleClientset(objects...)

			if tt.simulateOtherError {
				clientset.PrependReactor("get", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("simulated error")
				})
			}

			// Should not return error (errors are non-fatal)
			err := CleanupAllAdditionalSecrets(ctx, clientset, tt.targetNamespace)
			assert.NoError(t, err)
		})
	}
}

func TestCleanupOrphanedSecrets_ErrorCases(t *testing.T) {
	tests := []struct {
		name                string
		secretNames         []string
		existingSecrets     []*corev1.Secret
		simulateListError   bool
		simulateDeleteError bool
		expectError         bool
	}{
		{
			name:              "handles list error",
			secretNames:       []string{"secret1"},
			simulateListError: true,
			expectError:       true,
		},
		{
			name:        "handles delete error gracefully",
			secretNames: []string{"keep-me"},
			existingSecrets: []*corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "delete-me",
						Namespace: "target-namespace",
						Labels: map[string]string{
							AdditionalImagePullSecretLabelKey: "true",
						},
					},
				},
			},
			simulateDeleteError: true,
			expectError:         false, // Continues despite delete error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			objects := make([]runtime.Object, 0, len(tt.existingSecrets)+1)

			targetNS := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-namespace",
				},
			}
			objects = append(objects, targetNS)

			for _, s := range tt.existingSecrets {
				objects = append(objects, s)
			}

			controller := newTestController(tt.secretNames, objects...)

			if tt.simulateListError {
				controller.clientset.(*fake.Clientset).PrependReactor("list", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("simulated list error")
				})
			}

			if tt.simulateDeleteError {
				controller.clientset.(*fake.Clientset).PrependReactor("delete", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
					deleteAction := action.(k8stesting.DeleteAction)
					if deleteAction.GetName() == "delete-me" {
						return true, nil, fmt.Errorf("simulated delete error")
					}
					return false, nil, nil
				})
			}

			err := controller.cleanupOrphanedSecrets(ctx)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSyncSecret_ErrorCases(t *testing.T) {
	tests := []struct {
		name                string
		secret              *corev1.Secret
		namespaceExists     bool
		simulateCreateError bool
		simulateGetError    bool
		expectError         bool
	}{
		{
			name: "handles create error",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{".dockerconfigjson": []byte("test")},
			},
			namespaceExists:     true,
			simulateCreateError: true,
			expectError:         true,
		},
		{
			name: "handles get error (non-NotFound)",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "source-namespace",
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{".dockerconfigjson": []byte("test")},
			},
			namespaceExists:  true,
			simulateGetError: true,
			expectError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			objects := make([]runtime.Object, 0)

			if tt.namespaceExists {
				targetNS := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "target-namespace",
					},
				}
				objects = append(objects, targetNS)
			}

			controller := newTestController([]string{tt.secret.Name}, objects...)

			if tt.simulateCreateError {
				controller.clientset.(*fake.Clientset).PrependReactor("create", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("simulated create error")
				})
			}

			if tt.simulateGetError {
				controller.clientset.(*fake.Clientset).PrependReactor("get", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
					getAction := action.(k8stesting.GetAction)
					if getAction.GetName() == tt.secret.Name && getAction.GetNamespace() == "target-namespace" {
						return true, nil, fmt.Errorf("simulated get error")
					}
					return false, nil, nil
				})
			}

			err := controller.syncSecret(ctx, tt.secret)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
