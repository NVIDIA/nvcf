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

package testsupport

import (
	"context"
	"errors"
	"testing"
)

func TestRunWithWaitJoinsWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	waitStarted := make(chan struct{})
	finishWait := make(chan struct{})
	returned := make(chan struct{})
	results := make(chan [2]error, 1)
	waitErr := errors.New("mock wait failed")

	go func() {
		runErr, resultErr := RunWithWait(ctx, cancel,
			func(context.Context) error { return nil },
			func(context.Context) error {
				close(waitStarted)
				<-finishWait
				return waitErr
			},
		)
		results <- [2]error{runErr, resultErr}
		close(returned)
	}()

	<-waitStarted
	select {
	case <-returned:
		t.Fatal("returned before the mock server waiter finished")
	default:
	}

	close(finishWait)
	<-returned
	result := <-results
	if result[0] != nil {
		t.Fatalf("run error = %v, want nil", result[0])
	}
	if !errors.Is(result[1], waitErr) {
		t.Fatalf("wait error = %v, want %v", result[1], waitErr)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want context.Canceled", ctx.Err())
	}
}
