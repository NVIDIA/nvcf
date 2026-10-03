/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package clustervalidator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Every check reads the cluster through observe, and every read that still
// fails is reported through readFailure or unknownWarning. A failed read is
// never evidence about the thing being checked: the row it decides is left
// unknown (a nil result) with a warning naming the resource and the cause.

// observeTimeout bounds how long observe retries one call, and
// observeRetryInterval is the wait between attempts. Vars so tests need not
// wait them out.
var (
	observeTimeout       = 30 * time.Second
	observeRetryInterval = 2 * time.Second
)

// retryable reports whether a failed call may succeed if repeated. Throttling,
// timeouts, apiserver or webhook unavailability and transport errors may
// clear. A denial, a rejected token, and answers about the object itself
// (absent, already present, invalid) will not.
func retryable(err error) bool {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err),
		apierrors.IsNotFound(err), apierrors.IsAlreadyExists(err),
		apierrors.IsInvalid(err), apierrors.IsBadRequest(err),
		apierrors.IsMethodNotSupported(err):
		return false
	}
	return true
}

// observe calls fn until it succeeds or fails in a way a retry cannot change,
// for at most observeTimeout and never past ctx. Each attempt is bounded by
// attemptContext, so one hung request cannot spend the whole budget.
func observe[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	deadline := time.Now().Add(observeTimeout)
	for {
		attemptCtx, cancel := attemptContext(ctx, deadline)
		v, err := fn(attemptCtx)
		cancel()
		if err == nil || !retryable(err) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return v, err
		}
		if !sleepCtx(ctx, observeRetryInterval) {
			return v, err
		}
	}
}

// observeErr is observe for a call that returns only an error.
func observeErr(ctx context.Context, fn func(context.Context) error) error {
	_, err := observe(ctx, func(c context.Context) (struct{}, error) { return struct{}{}, fn(c) })
	return err
}

// sleepCtx waits for d, and reports false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// withContext runs fn, which takes no context, and returns early with
// ctx.Err() when ctx ends first. client-go's discovery methods take no
// context, so without this a stalled aggregated API could hold a run past
// its deadline. fn keeps running until the client's request timeout.
func withContext[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := fn()
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// readAdvice is the next step for a failed read. Only a denial is about RBAC:
// granting access fixes nothing when the apiserver is throttling or down.
func readAdvice(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "grant the cluster-validator ServiceAccount get and list on it"
	case apierrors.IsUnauthorized(err):
		return "the cluster-validator ServiceAccount token was rejected"
	default:
		return "check apiserver health and re-run"
	}
}

// readFailure describes a failed read of resource, with its cause and the
// advice that matches the cause.
func readFailure(resource string, err error) string {
	return fmt.Sprintf("could not read %s: %v; %s", resource, err, readAdvice(err))
}

// unknownWarning is the warning for a row left unknown because resource could
// not be read.
func unknownWarning(row, resource string, err error) string {
	return row + ": status unknown (" + readFailure(resource, err) + ")"
}

// readFailures joins one entry per unread place, and the advice for each
// distinct cause.
func readFailures(entries []string, errs []error) string {
	var advice []string
	for _, err := range errs {
		if a := readAdvice(err); !slices.Contains(advice, a) {
			advice = append(advice, a)
		}
	}
	return strings.Join(entries, ", ") + "; " + strings.Join(advice, "; ")
}
