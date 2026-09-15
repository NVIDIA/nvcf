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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testResolvedModelCacheStorage(transition string) *ModelCacheStorageSelection {
	selection := &ModelCacheStorageSelection{
		StorageClassName:    DefaultModelCacheStorageClassName,
		StorageClassUID:     types.UID("storage-class-uid"),
		StorageClassDigest:  "v1:sha256:storage-class-digest",
		ProfileDigest:       "sha256:catalog-digest",
		Provider:            "nvmesh",
		Provisioner:         NVMeshStorageClassProvisioner,
		Transition:          transition,
		RequiredAccessModes: requiredAccessModesForTransition(transition),
	}
	if transition == ModelCacheTransitionROXReadOnly {
		selection.RequiredMountOptions = []string{"ro", "norecovery", "nouuid"}
	}
	if transition == ModelCacheTransitionRWXReadOnly {
		selection.Provider = "sharedFilesystem"
		selection.Provisioner = "shared.csi.example.com"
	}
	return selection
}

func testPersistedModelCacheStorageSelection(
	mode ModelCacheSelectionMode,
	transition string,
) *PersistedModelCacheStorageSelection {
	resolved := testResolvedModelCacheStorage(transition)
	return &PersistedModelCacheStorageSelection{
		Version:              modelCacheStorageSelectionVersion,
		Workflow:             ModelCacheWorkflowRegular,
		Mode:                 mode,
		StorageClassName:     resolved.StorageClassName,
		StorageClassUID:      resolved.StorageClassUID,
		StorageClassDigest:   resolved.StorageClassDigest,
		ProfileDigest:        resolved.ProfileDigest,
		Provider:             resolved.Provider,
		Provisioner:          resolved.Provisioner,
		Transition:           resolved.Transition,
		RequiredAccessModes:  append([]corev1.PersistentVolumeAccessMode(nil), resolved.RequiredAccessModes...),
		RequiredMountOptions: append([]string(nil), resolved.RequiredMountOptions...),
	}
}

func TestNewPersistedModelCacheStorageSelection(t *testing.T) {
	tests := []struct {
		name       string
		workflow   ModelCacheWorkflow
		mode       ModelCacheSelectionMode
		resolved   *ModelCacheStorageSelection
		wantErr    string
		wantFields bool
	}{
		{
			name:     "regular cache disabled without a StorageClass",
			workflow: ModelCacheWorkflowRegular,
			mode:     ModelCacheSelectionNone,
		},
		{
			name:     "Helm cache falls back without a StorageClass",
			workflow: ModelCacheWorkflowHelm,
			mode:     ModelCacheSelectionEphemeral,
		},
		{
			name:       "durable NVMesh",
			workflow:   ModelCacheWorkflowRegular,
			mode:       ModelCacheSelectionDurable,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionROXReadOnly),
			wantFields: true,
		},
		{
			name:       "durable regular rwxReadOnly",
			workflow:   ModelCacheWorkflowRegular,
			mode:       ModelCacheSelectionDurable,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionRWXReadOnly),
			wantFields: true,
		},
		{
			// A shared claim reaches other namespaces, so it serves Helm
			// caching as well as regular caching.
			name:       "Helm rwxReadOnly is accepted",
			workflow:   ModelCacheWorkflowHelm,
			mode:       ModelCacheSelectionDurable,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionRWXReadOnly),
			wantFields: true,
		},
		{
			name:       "catalog-disabled regular cache",
			workflow:   ModelCacheWorkflowRegular,
			mode:       ModelCacheSelectionNone,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionDisabled),
			wantFields: true,
		},
		{
			name:       "catalog-disabled Helm cache",
			workflow:   ModelCacheWorkflowHelm,
			mode:       ModelCacheSelectionEphemeral,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionDisabled),
			wantFields: true,
		},
		{
			name:     "durable selection without resolved storage",
			workflow: ModelCacheWorkflowRegular,
			mode:     ModelCacheSelectionDurable,
			wantErr:  "has no resolved storage",
		},
		{
			name:       "durable selection with disabled transition",
			workflow:   ModelCacheWorkflowRegular,
			mode:       ModelCacheSelectionDurable,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionDisabled),
			wantErr:    "unsupported transition",
			wantFields: true,
		},
		{
			name:       "non-durable selection with NVMesh transition",
			workflow:   ModelCacheWorkflowRegular,
			mode:       ModelCacheSelectionNone,
			resolved:   testResolvedModelCacheStorage(ModelCacheTransitionROXReadOnly),
			wantErr:    "non-durable model cache selection has transition",
			wantFields: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := NewPersistedModelCacheStorageSelection(tt.workflow, tt.mode, tt.resolved)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, selection)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, modelCacheStorageSelectionVersion, selection.Version)
			assert.Equal(t, tt.workflow, selection.Workflow)
			assert.Equal(t, tt.mode, selection.Mode)
			if !tt.wantFields {
				assert.Empty(t, selection.StorageClassName)
				assert.Empty(t, selection.StorageClassUID)
				assert.Empty(t, selection.StorageClassDigest)
				assert.Empty(t, selection.ProfileDigest)
				assert.Empty(t, selection.Provider)
				assert.Empty(t, selection.Provisioner)
				assert.Empty(t, selection.Transition)
				return
			}
			assert.Equal(t, tt.resolved.StorageClassName, selection.StorageClassName)
			assert.Equal(t, tt.resolved.StorageClassUID, selection.StorageClassUID)
			assert.Equal(t, tt.resolved.StorageClassDigest, selection.StorageClassDigest)
			assert.Equal(t, tt.resolved.ProfileDigest, selection.ProfileDigest)
			assert.Equal(t, tt.resolved.Provider, selection.Provider)
			assert.Equal(t, tt.resolved.Provisioner, selection.Provisioner)
			assert.Equal(t, tt.resolved.Transition, selection.Transition)
			assert.Equal(t, tt.resolved.RequiredMountOptions, selection.RequiredMountOptions)
		})
	}
}

func TestPersistedModelCacheStorageSelectionRWXReadOnlyContract(t *testing.T) {
	selection := testPersistedModelCacheStorageSelection(
		ModelCacheSelectionDurable, ModelCacheTransitionRWXReadOnly)
	require.NoError(t, selection.Validate())
	assert.False(t, selection.EncryptionRequired)
	assert.Equal(t,
		[]corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		selection.RequiredAccessModes)
	assert.Empty(t, selection.RequiredMountOptions)

	raw, err := selection.Marshal()
	require.NoError(t, err)
	parsed, err := ParsePersistedModelCacheStorageSelection(raw)
	require.NoError(t, err)
	assert.Equal(t, selection, parsed)

	encrypted := *selection
	encrypted.EncryptionRequired = true
	require.ErrorContains(t, encrypted.Validate(), "does not support encryption")

	helm := *selection
	helm.Workflow = ModelCacheWorkflowHelm
	require.NoError(t, helm.Validate(),
		"a shared claim serves Helm caching, which reaches other namespaces")

	extraMode := *selection
	extraMode.RequiredAccessModes = []corev1.PersistentVolumeAccessMode{
		corev1.ReadWriteMany,
		corev1.ReadOnlyMany,
	}
	require.ErrorContains(t, extraMode.Validate(), "requires access modes [ReadWriteMany]")

	withReaderOptions := *selection
	withReaderOptions.RequiredMountOptions = []string{"ro"}
	require.ErrorContains(t, withReaderOptions.Validate(), "does not create a reader PV")
}

func TestPersistedModelCacheStorageSelectionMarshalParseRoundTrip(t *testing.T) {
	want := testPersistedModelCacheStorageSelection(ModelCacheSelectionDurable, ModelCacheTransitionROXReadOnly)
	want.Workflow = ModelCacheWorkflowHelm

	raw, err := want.Marshal()
	require.NoError(t, err)
	assert.True(t, json.Valid([]byte(raw)))

	got, err := ParsePersistedModelCacheStorageSelection(raw + "\n\t")
	require.NoError(t, err)
	assert.Equal(t, want, got)

	remarshaled, err := got.Marshal()
	require.NoError(t, err)
	assert.Equal(t, raw, remarshaled, "the persisted annotation must have stable field ordering")
}

// TestPersistedModelCacheStorageSelectionAcceptsAnyQualifiedProvider pins the
// vendor-agnostic contract: a durable roxReadOnly selection is validated on the
// shape it needs, not on which vendor produced it. The catalog is what decides
// whether a driver may claim the transition.
func TestPersistedModelCacheStorageSelectionAcceptsAnyQualifiedProvider(t *testing.T) {
	selection := testPersistedModelCacheStorageSelection(
		ModelCacheSelectionDurable, ModelCacheTransitionROXReadOnly)
	selection.Provider = "weka"
	selection.Provisioner = "csi.weka.io"

	require.NoError(t, selection.Validate())
}

func TestParsePersistedModelCacheStorageSelectionStrict(t *testing.T) {
	const minimal = `{"version":"v1alpha1","workflow":"regularModelCache","mode":"none"}`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: "EOF"},
		{name: "malformed", raw: "{", want: "unexpected EOF"},
		{
			name: "unknown field",
			raw:  `{"version":"v1alpha1","workflow":"regularModelCache","mode":"none","backend":"nvmesh"}`,
			want: `unknown field "backend"`,
		},
		{name: "multiple values", raw: minimal + ` {}`, want: "multiple JSON values"},
		{name: "trailing junk", raw: minimal + " x", want: "invalid character"},
		{name: "wrong JSON type", raw: `[]`, want: "cannot unmarshal array"},
		{name: "null", raw: `null`, want: "unsupported model cache storage selection version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection, err := ParsePersistedModelCacheStorageSelection(tt.raw)
			require.ErrorContains(t, err, tt.want)
			assert.Nil(t, selection)
		})
	}
}

func TestPersistedModelCacheStorageSelectionValidate(t *testing.T) {
	validDurable := func() *PersistedModelCacheStorageSelection {
		return testPersistedModelCacheStorageSelection(ModelCacheSelectionDurable, ModelCacheTransitionROXReadOnly)
	}
	validDisabled := func() *PersistedModelCacheStorageSelection {
		return testPersistedModelCacheStorageSelection(ModelCacheSelectionNone, ModelCacheTransitionDisabled)
	}
	tests := []struct {
		name      string
		selection func() *PersistedModelCacheStorageSelection
		want      string
	}{
		{name: "nil", selection: func() *PersistedModelCacheStorageSelection { return nil }, want: "is nil"},
		{name: "unsupported version", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Version = "v2"
			return s
		}, want: "unsupported model cache storage selection version"},
		{name: "empty workflow", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Workflow = ""
			return s
		}, want: "invalid model cache workflow"},
		{name: "unknown workflow", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Workflow = "containerCache"
			return s
		}, want: "invalid model cache workflow"},
		{name: "empty mode", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Mode = ""
			return s
		}, want: "invalid model cache selection mode"},
		{name: "unknown mode", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Mode = "shared"
			return s
		}, want: "invalid model cache selection mode"},
		{name: "regular workflow with ephemeral mode", selection: func() *PersistedModelCacheStorageSelection {
			s := validDisabled()
			s.Mode = ModelCacheSelectionEphemeral
			return s
		}, want: "ephemeral model cache selection requires Helm workflow"},
		{name: "durable without resolved storage", selection: func() *PersistedModelCacheStorageSelection {
			return &PersistedModelCacheStorageSelection{
				Version:  modelCacheStorageSelectionVersion,
				Workflow: ModelCacheWorkflowRegular,
				Mode:     ModelCacheSelectionDurable,
			}
		}, want: "has no resolved storage"},
		{name: "wrong StorageClass", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.StorageClassName = "another-class"
			return s
		}, want: `StorageClass must be "nvcf-sc"`},
		{name: "missing StorageClass UID", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.StorageClassUID = ""
			return s
		}, want: "incomplete resolved storage"},
		{name: "missing StorageClass digest", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.StorageClassDigest = ""
			return s
		}, want: "incomplete resolved storage"},
		{name: "missing catalog digest", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.ProfileDigest = ""
			return s
		}, want: "incomplete resolved storage"},
		{name: "missing provider", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Provider = ""
			return s
		}, want: "incomplete resolved storage"},
		{name: "whitespace provider", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Provider = " \t"
			return s
		}, want: "incomplete resolved storage"},
		{name: "missing provisioner", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Provisioner = ""
			return s
		}, want: "incomplete resolved storage"},
		{name: "roxReadOnly reader mount must be read-only", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.RequiredMountOptions = []string{"norecovery", "nouuid"}
			return s
		}, want: `requires mount option "ro"`},
		{name: "missing transition", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Transition = ""
			return s
		}, want: "incomplete resolved storage"},
		{name: "durable disabled transition", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Transition = ModelCacheTransitionDisabled
			return s
		}, want: "unsupported transition"},
		{name: "durable unknown transition", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.Transition = "shared-filesystem"
			return s
		}, want: "unsupported transition"},

		{name: "duplicate required reader mount option", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.RequiredMountOptions = append(s.RequiredMountOptions, "ro")
			return s
		}, want: `duplicate required mount option "ro"`},
		{name: "conflicting required reader mount options", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.RequiredMountOptions = append(s.RequiredMountOptions, "rw")
			return s
		}, want: "required mount options \"ro\" and \"rw\" conflict"},
		{name: "blank required reader mount option", selection: func() *PersistedModelCacheStorageSelection {
			s := validDurable()
			s.RequiredMountOptions = append(s.RequiredMountOptions, " ")
			return s
		}, want: "invalid required mount option"},
		{name: "non-durable with required mount options", selection: func() *PersistedModelCacheStorageSelection {
			s := validDisabled()
			s.RequiredMountOptions = []string{"ro"}
			return s
		}, want: "non-durable model cache selection has required mount options"},
		{name: "none with durable transition", selection: func() *PersistedModelCacheStorageSelection {
			s := validDisabled()
			s.Transition = ModelCacheTransitionROXReadOnly
			return s
		}, want: "non-durable model cache selection has transition"},
		{name: "ephemeral with unknown transition", selection: func() *PersistedModelCacheStorageSelection {
			s := validDisabled()
			s.Workflow = ModelCacheWorkflowHelm
			s.Mode = ModelCacheSelectionEphemeral
			s.Transition = "shared-filesystem"
			return s
		}, want: "non-durable model cache selection has transition"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection := tt.selection()
			require.ErrorContains(t, selection.Validate(), tt.want)

			if selection != nil {
				_, err := selection.Marshal()
				require.ErrorContains(t, err, tt.want)
			}
		})
	}
}
