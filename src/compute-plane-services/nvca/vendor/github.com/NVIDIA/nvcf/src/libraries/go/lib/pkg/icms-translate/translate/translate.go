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

package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
)

type CreationQueueMessageMetadataGetter interface {
	GetCreationQueueMessageMetadata() common.CreationQueueMessageMetadata
}

func DecodeCreationQueueMessage(m []byte) (msg CreationQueueMessageMetadataGetter, err error) {
	var spec struct {
		// Function info may be top-level or within "functionDetails".
		FunctionID        string           `json:"functionId,omitempty"`
		FunctionVersionID string           `json:"functionVersionId,omitempty"`
		FunctionDetails   function.Details `json:"functionDetails,omitempty"`

		TaskDetails task.Details `json:"taskDetails,omitempty"`
	}
	if err := json.Unmarshal(m, &spec); err != nil {
		return nil, fmt.Errorf("decode to type-check message: %v", err)
	}

	// Some metadata for tasks/functions are top-level or launchSpecification.
	// The metadata type can be reused to extract then merge them.
	var lsMetadata struct {
		LS struct {
			common.CreationQueueMessageMetadata `json:",inline"`
		} `json:"launchSpecification"`
	}
	if err := json.Unmarshal(m, &lsMetadata); err != nil {
		return nil, fmt.Errorf("decode to type-check message: %v", err)
	}

	mdec := json.NewDecoder(bytes.NewReader(m))
	switch {
	case spec.FunctionID != "" || spec.FunctionDetails != (function.Details{}):
		m := function.CreationQueueMessage{}
		if err := mdec.Decode(&m); err != nil {
			return nil, fmt.Errorf("decode function message: %w", err)
		}
		//nolint:staticcheck // QF1008: embedded field selector required for clarity
		if err := m.CreationQueueMessageMetadata.Merge(lsMetadata.LS.CreationQueueMessageMetadata); err != nil {
			return nil, fmt.Errorf("merge function message metadata: %w", err)
		}

		if m.LaunchSpecification != nil {
			m.GPUType = m.LaunchSpecification.GPUName
		}
		// When the GPUType is not set, parse it from the InstanceType
		if m.GPUType == "" {
			//nolint:staticcheck // SA1019: deprecated field used for backward compatibility
			gpuT, err := parseGPUTypeFromInstanceType(m.InstanceType)
			if err != nil {
				return nil, err
			}
			m.GPUType = gpuT
		}
		if spec.FunctionID != "" && m.Details.FunctionID == "" {
			m.Details.FunctionID = spec.FunctionID
		}
		if spec.FunctionVersionID != "" && m.Details.FunctionVersionID == "" {
			m.Details.FunctionVersionID = spec.FunctionVersionID
		}
		spec.FunctionID = ""
		spec.FunctionVersionID = ""
		msg = m
	case spec.TaskDetails != (task.Details{}):
		m := task.CreationQueueMessage{}
		if err := mdec.Decode(&m); err != nil {
			return nil, fmt.Errorf("decode task message: %w", err)
		}
		//nolint:staticcheck // QF1008: embedded field selector required for clarity
		if err := m.CreationQueueMessageMetadata.Merge(lsMetadata.LS.CreationQueueMessageMetadata); err != nil {
			return nil, fmt.Errorf("merge task message metadata: %w", err)
		}
		msg = m
	default:
		return nil, fmt.Errorf("unknown message type, no function or task details present")
	}

	return msg, nil
}

func parseGPUTypeFromInstanceType(instanceType string) (string, error) {
	instanceParts := strings.Split(instanceType, ".")
	if len(instanceParts) != 3 {
		return "", fmt.Errorf("failed to parse the GPU name from the instanceType: %s", instanceType)
	}
	// Ex: ON-PREM.GPU.H100 -> H100
	return instanceParts[2], nil
}
