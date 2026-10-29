// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Class says what a failed operation may have done at the destination. It
// decides whether the same work can be sent again.
type Class string

const (
	// ClassRetryable means nothing landed, so the same work can be sent
	// again after a backoff.
	ClassRetryable Class = "retryable"
	// ClassTerminal means sending the same work again fails the same way.
	ClassTerminal Class = "terminal"
	// ClassAmbiguous means the operation may or may not have landed, such as
	// a timeout after the request was sent. A read has no effect, so an
	// ambiguous read is handled like a retryable one.
	ClassAmbiguous Class = "ambiguous"
)

// Scope says how much work a fault affects.
type Scope string

const (
	// ScopeSegment means the fault belongs to the submitted data. Other
	// work is unaffected.
	ScopeSegment Scope = "segment"
	// ScopeBackend means the destination or the path to it is failing, so
	// every submit is affected. Nothing is quarantined for it.
	ScopeBackend Scope = "backend"
	// ScopeUnknown means it is not yet clear which of the two applies.
	ScopeUnknown Scope = "unknown"
)

// Reason is a short code for a fault. It comes from a small fixed set, so it
// is safe as a metric attribute and as the key that correlates faults across
// segments. A backend may define its own reasons under the same rule: never
// an id, a path, a payload, or error text.
type Reason string

const (
	// ReasonAuth means the destination refused the credentials. Backends use
	// it for every credential refusal, because it is always handled as
	// backend scope, whatever the Fault's Scope says: hold everything, never
	// split a batch, never quarantine.
	ReasonAuth Reason = "auth"
	// ReasonRateLimit means the destination asked the caller to slow down.
	ReasonRateLimit Reason = "rate_limit"
	// ReasonTimeout means the call ran out of time.
	ReasonTimeout Reason = "timeout"
	// ReasonCanceled means the caller canceled the call.
	ReasonCanceled Reason = "canceled"
	// ReasonUnavailable means the destination or the network failed.
	ReasonUnavailable Reason = "unavailable"
	// ReasonRejected means the destination refused the request.
	ReasonRejected Reason = "rejected"
	// ReasonTooLarge means a file is over the backend's size limit.
	ReasonTooLarge Reason = "too_large"
	// ReasonUnknown means nothing recognized the error.
	ReasonUnknown Reason = "unknown"
)

// Fault is a classified backend error. A backend returns one, wrapped or not,
// when it knows what a failure means; Classify builds one for any other error.
// The zero Fault, with an empty Class, means no fault.
type Fault struct {
	Class  Class
	Scope  Scope
	Reason Reason
	// RetryAfter is how long the destination asked the caller to wait, for
	// example from a Retry-After header. Zero means it did not say.
	RetryAfter time.Duration
	Err        error
}

// Error describes the fault and its cause. The cause can carry remote text,
// ids or paths, so logs and metrics use Class, Scope and Reason instead.
func (f *Fault) Error() string {
	text := fmt.Sprintf("backend fault (class=%s scope=%s reason=%s)", f.Class, f.Scope, f.Reason)
	if f.Err == nil {
		return text
	}
	return text + ": " + f.Err.Error()
}

// Unwrap returns the cause.
func (f *Fault) Unwrap() error {
	return f.Err
}

func asFault(err error) (Fault, bool) {
	var fault *Fault
	if errors.As(err, &fault) && fault != nil {
		return *fault, true
	}
	return Fault{}, false
}

// httpStatusError is implemented by HTTP client errors that expose the
// response status, such as the AWS SDK's response errors.
type httpStatusError interface {
	HTTPStatusCode() int
}

// ClassifyDefault maps an error that no backend classified. It reads only
// generic signals: a Fault already in the chain, context errors, an HTTP
// status, and network errors. Anything else is retryable with unknown scope,
// so correlation across segments decides its scope.
//
// A failure is retryable only when it shows that nothing was processed: a
// failed dial or name lookup, 408, 429 or 503. A timeout, a broken connection
// after the request went out, and other 5xx responses are ambiguous, because
// the destination may have acted before the failure. A backend that knows
// better says so through its Classifier.
func ClassifyDefault(err error) Fault {
	if err == nil {
		return Fault{}
	}
	if fault, ok := asFault(err); ok {
		return fault
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) && sentNothing(err):
		// net reports a dial cut short by the caller's deadline as a dial
		// error that matches context.DeadlineExceeded. Nothing was sent.
		return Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonUnavailable, Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return Fault{Class: ClassAmbiguous, Scope: ScopeBackend, Reason: ReasonTimeout, Err: err}
	case errors.Is(err, context.Canceled):
		return Fault{Class: ClassAmbiguous, Scope: ScopeUnknown, Reason: ReasonCanceled, Err: err}
	}
	var status httpStatusError
	if errors.As(err, &status) {
		return classifyHTTPStatus(status.HTTPStatusCode(), err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		switch {
		case sentNothing(err):
			return Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonUnavailable, Err: err}
		case netErr.Timeout():
			return Fault{Class: ClassAmbiguous, Scope: ScopeBackend, Reason: ReasonTimeout, Err: err}
		default:
			return Fault{Class: ClassAmbiguous, Scope: ScopeBackend, Reason: ReasonUnavailable, Err: err}
		}
	}
	return Fault{Class: ClassRetryable, Scope: ScopeUnknown, Reason: ReasonUnknown, Err: err}
}

// sentNothing reports a network error from before any request byte left the
// host: a name lookup or a dial.
func sentNothing(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

func classifyHTTPStatus(code int, err error) Fault {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonAuth, Err: err}
	case code == http.StatusTooManyRequests:
		return Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonRateLimit, Err: err}
	case code == http.StatusRequestTimeout || code == http.StatusServiceUnavailable:
		return Fault{Class: ClassRetryable, Scope: ScopeBackend, Reason: ReasonUnavailable, Err: err}
	case code >= 500:
		return Fault{Class: ClassAmbiguous, Scope: ScopeBackend, Reason: ReasonUnavailable, Err: err}
	case code >= 400:
		return Fault{Class: ClassTerminal, Scope: ScopeUnknown, Reason: ReasonRejected, Err: err}
	default:
		return Fault{Class: ClassRetryable, Scope: ScopeUnknown, Reason: ReasonUnknown, Err: err}
	}
}

// ParseRetryAfter reads an HTTP Retry-After value, either delay seconds or an
// HTTP date, as the wait from now. A date in the past means no wait. It
// reports false for a value it cannot read, so a backend can leave
// Fault.RetryAfter at zero.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(date.Sub(now), 0), true
}
