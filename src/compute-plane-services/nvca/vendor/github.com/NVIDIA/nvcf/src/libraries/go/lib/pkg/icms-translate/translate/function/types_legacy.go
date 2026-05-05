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

package function

// +k8s:deepcopy-gen=true
type LaunchArtifacts []LaunchArtifact

type LaunchArtifact struct {
	Type          LaunchArtifactType `json:"artifactType,omitempty"`
	Specification string             `json:"artifactSpec,omitempty"`
}

type LaunchArtifactType string

const (
	LaunchArtifactTypePod          LaunchArtifactType = "POD_SPEC"
	LaunchArtifactTypeSecret       LaunchArtifactType = "SECRET_SPEC"
	LaunchArtifactTypeConfigmap    LaunchArtifactType = "CONFIGMAP_SPEC"
	LaunchArtifactTypeInitCacheJob LaunchArtifactType = "INIT_CACHE_JOB_SPEC"
	LaunchArtifactTypeBlockDevice  LaunchArtifactType = "BD_CREATE_SPEC"
	LaunchArtifactTypeHelmChart    LaunchArtifactType = "HELM_CHART"
	LaunchArtifactTypeHelmCreds    LaunchArtifactType = "HELM_CREDS"
)
