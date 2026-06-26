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

package utils

import (
	"context"
	"testing"
	"time"
)

func TestSleepWithContext(t *testing.T) {
	t.Run("returns promptly when context already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() {
			SleepWithContext(ctx, time.Hour) // would block an hour if it ignored ctx
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("SleepWithContext did not return promptly on a canceled context")
		}
	})
	t.Run("returns after the timer elapses when not canceled", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			SleepWithContext(context.Background(), 10*time.Millisecond)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("SleepWithContext did not return after its timer elapsed")
		}
	})
}
