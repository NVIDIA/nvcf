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

import "errors"

// Central location for all user facing error messages related to task metadata
var (
	ErrInvalidLength                = errors.New("the name key length must be between 1 and 190 characters")
	ErrInvalidPrefix                = errors.New("the name key must not start with './' or '../'")
	ErrInvalidCharacters            = errors.New("invalid characters found in the name key. Only alphanumeric and !-_.*'() are allowed")
	ErrDuplicateResultName          = errors.New("duplicate name key between consecutive progress updates")
	ErrInvalidTaskId                = errors.New("task ID in progress file does not match task ID returned by NVCT")
	ErrInvalidTimestamp             = errors.New("invalid timestamp format, require ISO8601")
	ErrOutOfRangePercentageComplete = errors.New("invalid percentComplete. Only integers from 1 to 100 are allowed")
	ErrInvalidPercentageComplete    = errors.New("invalid value of percentComplete. Progress cannot decrease from previous reported value")
	ErrNoProgressFileUpdates        = errors.New("no update for 'lastUpdatedAt' field in progress file")
	ErrMaxRunTimeDurationExceeded   = errors.New("task execution time exceeds the limit")
	ErrMissingUploadDir             = errors.New("failed to find directory to upload")
	ErrNgcAuthFailure               = errors.New("authorization failure with NGC_API_KEY")
	ErrFailedToUploadResults        = errors.New("failed to upload some results")
	ErrInternal                     = errors.New("failed to process task due to internal error")
	ErrSecretSetup                  = errors.New("failed to set up worker secrets")
	ErrMissingSecret                = errors.New("missing NGC api key for result uploading")
	ErrInvalidProgressFile          = errors.New("invalid schema of progress file")
	ErrMissingProgressFile          = errors.New("missing progress file from task container")
	ErrTaskNotComplete              = errors.New("task has not completed when terminating")
	ErrTaskNotReady                 = errors.New("task container is not ready as progress file is not created")
	ErrWorkerTerminatedUngracefully = errors.New("worker failed to terminate gracefully")
)
