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

package upload

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

type MockUploader struct {
	Uploader
	wg        sync.WaitGroup
	semaphore chan struct{}
}

func NewMockUploader(workers int) *MockUploader {
	return &MockUploader{
		semaphore: make(chan struct{}, workers),
	}
}

func (u *MockUploader) Submit(ctx context.Context, resultPath string, modelVersion string) {
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		zap.L().Info("Job received")
		u.semaphore <- struct{}{}
		defer func() { <-u.semaphore }()

		duration := 2 * time.Second
		time.Sleep(duration)
		zap.L().Info("Job completed")

	}()
}

func (u *MockUploader) Wait() error {
	u.wg.Wait()
	return nil
}
