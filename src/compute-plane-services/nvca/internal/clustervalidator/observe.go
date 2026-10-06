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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8serr"
)

// Every check reads the cluster through observe, and every read that still
// fails is reported through readFailure or unknownWarning. A failed read is
// never evidence about the thing being checked: the row it decides is left
// unknown (a nil result) with a warning naming the resource and the cause.

// RequestTimeout bounds every apiserver request, including the discovery calls
// that take no context. The cluster-validator binary sets it as its client's
// timeout.
const RequestTimeout = 30 * time.Second

// defaultObserveTimeout is observe's window for one call. It leaves room for a
// full second attempt after one the client timed out, so a create the
// apiserver applied but answered too late is retried, answered AlreadyExists,
// and adopted by createOrAdopt.
const defaultObserveTimeout = 2*RequestTimeout + 10*time.Second

// observeTimeout bounds how long observe retries one call,
// observeAttemptTimeout caps one attempt at the client's request timeout, and
// observeRetryInterval is the wait between attempts. Vars so tests need not
// wait them out.
var (
	observeTimeout        = defaultObserveTimeout
	observeAttemptTimeout = RequestTimeout
	observeRetryInterval  = 2 * time.Second
)

// retryable reports whether a failed call may succeed if repeated: what
// k8serr.IsTransient calls transient, any other 5xx, such as a 502
// from a proxy in front of the apiserver, and an error that is no apiserver
// answer at all, such as a response body cut off mid-read. Any other answer,
// such as a denial or one about the object itself (absent, already present,
// invalid), will not change.
func retryable(err error) bool {
	if k8serr.IsTransient(err) {
		return true
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return status.Status().Code >= http.StatusInternalServerError
	}
	return true
}

// observe calls fn until it succeeds or fails in a way a retry cannot change,
// for at most observeTimeout and never past ctx. An attempt may use what is
// left of that budget up to the client's request timeout, not a poll loop's
// pollAttemptTimeout: a LIST on a large cluster can take longer than that cap,
// and every attempt cut off there fails the same way. The one-second floor is
// attemptContext's: an attempt that starts after a retry pause has crossed the
// deadline still gets an answer.
func observe[T any](ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	deadline := time.Now().Add(observeTimeout)
	for {
		attempt := min(max(time.Until(deadline), time.Second), observeAttemptTimeout)
		attemptCtx, cancel := context.WithTimeout(ctx, attempt)
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

// createOrAdopt creates, with create, an object named for this run. A create
// the apiserver applied after the client gave up on it is answered
// AlreadyExists when retried, so on AlreadyExists the object is read back with
// get and adopted when it carries this run's instance label. One without it is
// not this run's, and the AlreadyExists stands.
func createOrAdopt[T metav1.Object](
	ctx context.Context, instance string, create, get func(context.Context) (T, error),
) (T, error) {
	return observe(ctx, func(c context.Context) (T, error) {
		obj, err := create(c)
		if !apierrors.IsAlreadyExists(err) {
			return obj, err
		}
		existing, getErr := get(c)
		switch {
		case getErr == nil && existing.GetLabels()[instanceLabel] == instance:
			return existing, nil
		case getErr != nil && retryable(getErr):
			return obj, getErr
		default:
			return obj, err
		}
	})
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
