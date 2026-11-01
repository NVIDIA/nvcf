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

package errors

import (
	"fmt"
)

var _ error = &LaunchArtifactError{}

type LaunchArtifactErrorReason string

const (
	LaunchArtifactErrorReasonUnknown         LaunchArtifactErrorReason = ""
	LaunchArtifactErrorReasonTypesDoNotExist LaunchArtifactErrorReason = "TypesDoNotExist"
)

// LaunchArtifactError represents an error for the launch artifact
type LaunchArtifactError struct {
	Message string
	Reason  LaunchArtifactErrorReason
}

func (e *LaunchArtifactError) Error() string {
	return e.Message
}

func ReasonForError(err error) LaunchArtifactErrorReason {
	if artErr, ok := err.(*LaunchArtifactError); ok {
		return artErr.Reason
	}
	return LaunchArtifactErrorReasonUnknown
}

func IsTypesDoNotExist(err error) bool {
	return ReasonForError(err) == LaunchArtifactErrorReasonTypesDoNotExist
}

func NewTypesDoNotExist(artifactTypes ...string) *LaunchArtifactError {
	return &LaunchArtifactError{
		Message: fmt.Sprintf("unknown function launch artifact type: %s", artifactTypes),
		Reason:  LaunchArtifactErrorReasonTypesDoNotExist,
	}
}
