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

package types

import (
	"errors"
	"testing"
)

func TestErrorSentinels(t *testing.T) {
	tests := []struct {
		name string
		err  error
		msg  string
	}{
		{"ErrInvalidLength", ErrInvalidLength, "the name key length must be between 1 and 190 characters"},
		{"ErrInvalidPrefix", ErrInvalidPrefix, "the name key must not start with './' or '../'"},
		{"ErrInvalidCharacters", ErrInvalidCharacters, "invalid characters found in the name key. Only alphanumeric and !-_.*'() are allowed"},
		{"ErrDuplicateResultName", ErrDuplicateResultName, "duplicate name key between consecutive progress updates"},
		{"ErrInvalidTaskId", ErrInvalidTaskId, "task ID in progress file does not match task ID returned by NVCT"},
		{"ErrInvalidTimestamp", ErrInvalidTimestamp, "invalid timestamp format, require ISO8601"},
		{"ErrOutOfRangePercentageComplete", ErrOutOfRangePercentageComplete, "invalid percentComplete. Only integers from 1 to 100 are allowed"},
		{"ErrInvalidPercentageComplete", ErrInvalidPercentageComplete, "invalid value of percentComplete. Progress cannot decrease from previous reported value"},
		{"ErrNoProgressFileUpdates", ErrNoProgressFileUpdates, "no update for 'lastUpdatedAt' field in progress file"},
		{"ErrMaxRunTimeDurationExceeded", ErrMaxRunTimeDurationExceeded, "task execution time exceeds the limit"},
		{"ErrMissingUploadDir", ErrMissingUploadDir, "failed to find directory to upload"},
		{"ErrNgcAuthFailure", ErrNgcAuthFailure, "authorization failure with NGC_API_KEY"},
		{"ErrFailedToUploadResults", ErrFailedToUploadResults, "failed to upload some results"},
		{"ErrInternal", ErrInternal, "failed to process task due to internal error"},
		{"ErrSecretSetup", ErrSecretSetup, "failed to set up worker secrets"},
		{"ErrMissingSecret", ErrMissingSecret, "missing NGC api key for result uploading"},
		{"ErrInvalidProgressFile", ErrInvalidProgressFile, "invalid schema of progress file"},
		{"ErrMissingProgressFile", ErrMissingProgressFile, "missing progress file from task container"},
		{"ErrTaskNotComplete", ErrTaskNotComplete, "task has not completed when terminating"},
		{"ErrTaskNotReady", ErrTaskNotReady, "task container is not ready as progress file is not created"},
		{"ErrWorkerTerminatedUngracefully", ErrWorkerTerminatedUngracefully, "worker failed to terminate gracefully"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatalf("%s is nil", tt.name)
			}
			if tt.err.Error() != tt.msg {
				t.Errorf("%s message = %q, want %q", tt.name, tt.err.Error(), tt.msg)
			}
			// Each sentinel must match itself via errors.Is for wrap-aware callers.
			if !errors.Is(tt.err, tt.err) {
				t.Errorf("%s does not match itself via errors.Is", tt.name)
			}
		})
	}
}

func TestErrorSentinelsAreDistinct(t *testing.T) {
	all := []error{
		ErrInvalidLength, ErrInvalidPrefix, ErrInvalidCharacters, ErrDuplicateResultName,
		ErrInvalidTaskId, ErrInvalidTimestamp, ErrOutOfRangePercentageComplete,
		ErrInvalidPercentageComplete, ErrNoProgressFileUpdates, ErrMaxRunTimeDurationExceeded,
		ErrMissingUploadDir, ErrNgcAuthFailure, ErrFailedToUploadResults, ErrInternal,
		ErrSecretSetup, ErrMissingSecret, ErrInvalidProgressFile, ErrMissingProgressFile,
		ErrTaskNotComplete, ErrTaskNotReady, ErrWorkerTerminatedUngracefully,
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if errors.Is(all[i], all[j]) {
				t.Errorf("sentinels %d and %d must not match via errors.Is", i, j)
			}
		}
	}
}
