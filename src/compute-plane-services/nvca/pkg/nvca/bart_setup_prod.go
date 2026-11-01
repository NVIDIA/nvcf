//go:build !test

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

package nvca

import (
	"time"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
)

var (
	FailedRequestCleanupWindow           = 1 * time.Hour
	CachedRequestCleanupWindow           = 1 * time.Hour
	DefaultTerminationGracePeriodSeconds = 120 // 2 minutes

	syncICMSRequestInterval      = 15 * time.Second
	ackReqInterval               = 5 * time.Second
	syncICMSRegistrationInterval = 60 * time.Second
)

var (
	newTokenFetcher = nvcaauth.NewTokenFetcher
	newQueueClient  = defaultNewQueueClient
)
