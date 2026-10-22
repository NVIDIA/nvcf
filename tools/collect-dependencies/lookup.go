// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// lookupError keeps credentials and remote response bodies out of diagnostics
// while retaining the cause for error inspection.
type lookupError struct {
	reason    string
	temporary bool
	cause     error
}

func (e *lookupError) Error() string { return e.reason }
func (e *lookupError) Unwrap() error { return e.cause }

func retryLookup(timeout time.Duration, lookup func(time.Duration) error) error {
	deadline := time.Now().Add(timeout)
	for attempt := 1; ; attempt++ {
		err := lookup(time.Until(deadline))
		if err == nil {
			return nil
		}
		var failure *lookupError
		delay := time.Duration(attempt) * 100 * time.Millisecond
		if attempt == 3 || !errors.As(err, &failure) || !failure.temporary || time.Until(deadline) <= delay {
			return fmt.Errorf("lookup failed after %d attempt(s): %w", attempt, err)
		}
		time.Sleep(delay)
	}
}

func networkLookupError(err error) *lookupError {
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return &lookupError{reason: "network timeout", temporary: true, cause: err}
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return &lookupError{reason: "temporary connection failure", temporary: true, cause: err}
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) && dnsError.IsTemporary {
		return &lookupError{reason: "temporary DNS failure", temporary: true, cause: err}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &lookupError{reason: "incomplete network response", temporary: true, cause: err}
	}
	return &lookupError{reason: "network request failed; check registry connectivity and credentials", cause: err}
}

var transientGoStatus = regexp.MustCompile(`\b(?:429|500|502|503|504)\s+(?:too many requests|internal server error|bad gateway|service unavailable|gateway timeout)\b`)

func goLookupError(err error, output string) *lookupError {
	var missingTool *exec.Error
	if errors.As(err, &missingTool) {
		return &lookupError{reason: "Go executable unavailable; check PATH", cause: err}
	}
	text := strings.ToLower(output)
	// These failures must not become retryable merely because a proxy URL or
	// another line also contains a transient error.
	for _, marker := range []string{"checksum mismatch", "security error", "401 unauthorized", "403 forbidden", "404 not found", "unknown revision", "invalid version"} {
		if strings.Contains(text, marker) {
			return &lookupError{reason: marker, cause: err}
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || transientGoStatus.MatchString(text) {
		return &lookupError{reason: "temporary module lookup failure", temporary: true, cause: err}
	}
	for _, marker := range []string{"i/o timeout", "tls handshake timeout", "connection reset by peer", "temporary failure in name resolution", "unexpected eof"} {
		if strings.Contains(text, marker) {
			return &lookupError{reason: marker, temporary: true, cause: err}
		}
	}
	return &lookupError{reason: "Go command failed; check module graph, proxy access and toolchain", cause: err}
}

func runGoLicenseCommand(dir string, timeout time.Duration, args ...string) (string, error) {
	var stdout string
	err := retryLookup(timeout, func(remaining time.Duration) error {
		var stderr string
		var err error
		stdout, stderr, err = runCommand(dir, nil, remaining, "go", args...)
		if err != nil {
			return goLookupError(err, stdout+"\n"+stderr)
		}
		return nil
	})
	return stdout, err
}
