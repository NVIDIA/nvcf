//go:build test

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
	"context"
	"time"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	mockqueueservice "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/queueservice"
	mocktokencache "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/mock/tokencache"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/queue"
)

var (
	// To speed up testing
	FailedRequestCleanupWindow           = 2 * time.Second
	CachedRequestCleanupWindow           = 2 * time.Second
	DefaultTerminationGracePeriodSeconds = 1
	syncICMSRequestInterval              = 2 * time.Second
	ackReqInterval                       = 2 * time.Second
	syncICMSRegistrationInterval         = 2 * time.Second

	newTokenFetcher = func(_ context.Context, opts nvcaauth.TokenFetcherOptions) (TokenFetcher, error) {
		return mocktokencache.New("icmsservice", opts.SSAClientID, "/tmp/private.pem")
	}

	newQueueClient = func(_ string) queue.Client {
		return mockqueueservice.NewClient()
	}
)
