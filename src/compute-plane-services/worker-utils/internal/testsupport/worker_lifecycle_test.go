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

func TestRunWithWaitWaitsForAsyncCallbackBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	callbacks := NewCallbackGroup()
	callbackStarted := make(chan struct{})
	finishCallback := make(chan struct{})
	defer func() {
		select {
		case <-finishCallback:
		default:
			close(finishCallback)
		}
	}()
	callbackReported := make(chan struct{})
	serverDone := make(chan struct{})
	waitReturned := make(chan struct{})
	runErr := errors.New("worker run failed")
	callbackErr := errors.New("mock work failed")

	run := func(ctx context.Context) error {
		go func() {
			err := callbacks.Run(func() error {
				close(callbackStarted)
				<-ctx.Done()
				<-finishCallback
				return callbackErr
			})
			if err != nil {
				// This models the mock server's error reporting after
				// doWorkFunc returns and before its done channel is closed.
				close(callbackReported)
			}
			close(serverDone)
		}()
		<-callbackStarted
		return runErr
	}
	mockWait := func(ctx context.Context) error {
		select {
		case <-serverDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	wait := func(ctx context.Context) error {
		return WaitForTrackedCallback(ctx, func(ctx context.Context) error {
			err := mockWait(ctx)
			if ctx.Err() != nil {
				close(waitReturned)
			}
			return err
		}, callbacks)
	}

	returned := make(chan struct{})
	results := make(chan [2]error, 1)
	go func() {
		runErr, waitErr := RunWithWait(ctx, cancel, run, wait)
		results <- [2]error{runErr, waitErr}
		close(returned)
	}()
	<-waitReturned

	select {
	case <-returned:
		t.Fatal("returned before the asynchronous callback finished")
	default:
	}
	close(finishCallback)
	<-returned
	result := <-results
	if !errors.Is(result[0], runErr) {
		t.Fatalf("run error = %v, want %v", result[0], runErr)
	}
	if !errors.Is(result[1], context.Canceled) {
		t.Fatalf("wait error = %v, want context.Canceled", result[1])
	}
	select {
	case <-callbackReported:
	default:
		t.Fatal("returned before the mock server reported the callback error")
	}
}

func TestWaitForTrackedCallbackDoesNotWaitWhenCallbackNeverStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	callbacks := NewCallbackGroup()
	waitCalls := 0

	err := WaitForTrackedCallback(ctx, func(ctx context.Context) error {
		waitCalls++
		return ctx.Err()
	}, callbacks)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context.Canceled", err)
	}
	if waitCalls != 1 {
		t.Fatalf("wait calls = %d, want 1 when callback never starts", waitCalls)
	}

	callbackRan := false
	if err := callbacks.Run(func() error {
		callbackRan = true
		return nil
	}); err != nil {
		t.Fatalf("late callback error = %v, want nil", err)
	}
	if callbackRan {
		t.Fatal("callback started after teardown was sealed")
	}
}
