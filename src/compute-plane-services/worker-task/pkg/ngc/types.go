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

package ngc

// Reference: https://docs.ngc.nvidia.com/api/?urls.primaryName=Private%20Artifacts%20(Models)%20API#/
// ------------------------------------------------------------------------

type ModelCreateRequest struct {
	Name      string `json:"name"`
	Precision string `json:"precision"`
	Framework string `json:"framework"`
}

// ------------------------------------------------------------------------

type ModelVersionCreateRequest struct {
	VersionId string `json:"versionId"`
}

// ------------------------------------------------------------------------

type ModelUploadRequest struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	ArtifactType   string `json:"artifactType"`
	FilePath       string `json:"filePath"`
	Size           int64  `json:"size"`
	CustomPartSize int64  `json:"customPartSize"`
}

// ------------------------------------------------------------------------

type ModelUploadResponse struct {
	UploadId string   `json:"uploadID"`
	PartSize int      `json:"partSize"`
	Urls     []string `json:"urls"`
}

// ------------------------------------------------------------------------

type CompleteModelUploadRequest struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	ArtifactType string `json:"artifactType"`
	FilePath     string `json:"filePath"`
	FileHash     string `json:"sha256"`
	UploadId     string `json:"uploadID"`
}

// ------------------------------------------------------------------------

type AbortModelUploadRequest struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	ArtifactType string `json:"artifactType"`
	FilePath     string `json:"filePath"`
	UploadId     string `json:"uploadID"`
}

// ------------------------------------------------------------------------

type UpdateModelRequest struct {
	Status string `json:"status"`
}
