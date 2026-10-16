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

// Package testsupport contains helpers shared by worker-utils tests.
package testsupport

import "context"

// RunWithWait runs a worker and waits for its mock server to finish before
// returning. The waiter cancels the worker when its work completes; if the
// worker fails first, cancellation releases the waiter. Returning both errors
// lets callers report failures from the test goroutine rather than a helper
// goroutine.
func RunWithWait(
	ctx context.Context,
	cancel context.CancelFunc,
	run func(context.Context) error,
	wait func(context.Context) error,
) (runErr, waitErr error) {
	waitResult := make(chan error, 1)
	go func() {
		waitErr := wait(ctx)
		waitResult <- waitErr
		cancel()
	}()

	runErr = run(ctx)
	if runErr != nil {
		cancel()
	}
	waitErr = <-waitResult
	cancel()
	return runErr, waitErr
}
