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

package httpstream

// BackgroundOnce executes a function in a background goroutine and caches its result.
// The result can be retrieved multiple times, and each retrieval will wait for the
// function to complete if it hasn't already.
func BackgroundOnce[A, B any](f func() (A, B)) func() (A, B) {
	var (
		resultA A
		resultB B
		done    = make(chan struct{})
	)

	// Start the function in a background goroutine
	go func() {
		resultA, resultB = f()
		close(done)
	}()

	// Return a function that waits for and returns the result
	return func() (A, B) {
		// If the function hasn't completed yet, this will block until it does
		<-done
		return resultA, resultB
	}
}
