/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package imagecredential

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var (
	//go:embed testdata/secret.yaml
	testSecretData []byte
	//go:embed testdata/cronjob.yaml
	testCronJobData []byte
	//go:embed testdata/job.yaml
	testJobData []byte
)

func TestNewImageCredsSecret(t *testing.T) {
	type spec struct {
		name        string
		idComponent string
		allEnvSet   map[string]string
		expError    string
	}

	for _, tt := range []spec{
		{
			name:        "success",
			idComponent: "sr-ab93ff3b-a94a-4a7d-808f-54e1546a9fc4",
			allEnvSet: map[string]string{
				common.ContainerRegistriesCredentialsEnv: "blahblah",
				common.SidecarRegistryCredentialEnv:      "blahblah",
			},
		},
		{
			name:        "no id component",
			idComponent: "",
			allEnvSet: map[string]string{
				common.ContainerRegistriesCredentialsEnv: "blahblah",
				common.SidecarRegistryCredentialEnv:      "blahblah",
			},
			expError: "id component is required",
		},
		{
			name:        "no workload env",
			idComponent: "sr-ab93ff3b-a94a-4a7d-808f-54e1546a9fc4",
			allEnvSet:   map[string]string{},
			expError:    "env CONTAINER_REGISTRIES_CREDENTIALS not found",
		},
		{
			name:        "no worker env",
			idComponent: "sr-ab93ff3b-a94a-4a7d-808f-54e1546a9fc4",
			allEnvSet: map[string]string{
				common.ContainerRegistriesCredentialsEnv: "blahblah",
			},
			expError: "env SIDECAR_REGISTRY_CREDENTIAL not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gotSecret, gotErr := NewImageCredsSecret(tt.idComponent, tt.allEnvSet)
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
			} else if assert.NoError(t, gotErr) {
				testSecretDataJSON, err := yaml.ToJSON(testSecretData)
				require.NoError(t, err)
				gotSecret.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Secret"})
				gotSecret.SetCreationTimestamp(v1.Time{})
				gotSecretData, err := json.Marshal(gotSecret)
				require.NoError(t, err)
				assert.JSONEq(t, string(testSecretDataJSON), string(gotSecretData))
			}
		})
	}
}

func TestNewBatchObjects(t *testing.T) {
	imageRef := "example.com/nvcf/image-credential-helper:0.1.0"
	gotCronJob := NewUpdaterCronJob("image-cred-updater", imageRef, "foo=bar")
	gotJob := NewInitJob("sr-foo-image-cred-updater", imageRef, "sr-foo", "foo=bar")

	testCronJobDataJSON, err := yaml.ToJSON(testCronJobData)
	require.NoError(t, err)
	gotCronJob.SetGroupVersionKind(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"})
	gotCronJob.SetCreationTimestamp(v1.Time{})
	gotCronJobData, err := json.Marshal(gotCronJob)
	require.NoError(t, err)
	assert.JSONEq(t, string(testCronJobDataJSON), string(gotCronJobData))

	testJobDataJSON, err := yaml.ToJSON(testJobData)
	require.NoError(t, err)
	gotJob.SetGroupVersionKind(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
	gotJob.SetCreationTimestamp(v1.Time{})
	gotJobData, err := json.Marshal(gotJob)
	require.NoError(t, err)
	assert.JSONEq(t, string(testJobDataJSON), string(gotJobData))
}
