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

// Package shutdown tells a graceful stop apart from a crash.
//
// The servers run group models a shutdown signal as a terminal error, so an
// expected stop and a genuine failure both reach callers as ordinary errors.
// Every consumer therefore had to decide which was which, and each did it by
// matching the message text against the SIGINT wording. SIGTERM stringifies as
// "terminated" rather than "interrupt", so those checks classified every
// SIGTERM -- and SIGTERM is how Kubernetes asks a container to stop -- as a
// crash, turning routine pod terminations into panics.
//
// This package is the single place that decision lives. Producers report a
// signal with NewSignalError so consumers can use errors.Is; consumers call
// IsSignalError, which also recognizes the historical wording so it keeps
// working against builds that predate the typed error.
package shutdown

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// ErrSignal is the sentinel every shutdown-signal error matches under
// errors.Is, so callers can classify a stop without depending on the concrete
// error type or on its message.
var ErrSignal = errors.New("shutdown signal")

// signalErrorPrefix is the historical wording of the run group's terminal
// error. It is part of the contract: consumers pinned to a lib revision that
// predates this package, and consumers whose servers package comes from the
// separate nvcf-go module, only ever see the text.
const signalErrorPrefix = "received signal "

// handledSignals are the signals a server installs a handler for. SIGTERM is
// what Kubernetes sends to stop a container, so it is the one production
// actually sees; SIGINT only shows up in local runs.
var handledSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

// Signals returns the signals a server should stop on, for passing to
// signal.Notify. Sharing the list with IsSignalError keeps the set a server
// listens for and the set callers forgive from drifting apart.
func Signals() []os.Signal {
	return append([]os.Signal(nil), handledSignals...)
}

// SignalError reports that a server stopped because it received a shutdown
// signal rather than because something failed.
type SignalError struct {
	Signal os.Signal
}

// NewSignalError returns the error a server should report when sig stops it.
func NewSignalError(sig os.Signal) *SignalError {
	return &SignalError{Signal: sig}
}

// Error preserves the historical wording so consumers still matching on text
// keep working.
func (e *SignalError) Error() string {
	if e.Signal == nil {
		return strings.TrimSpace(signalErrorPrefix)
	}
	return signalErrorPrefix + e.Signal.String()
}

// Is reports SignalError as ErrSignal so errors.Is classifies any shutdown
// signal without the caller naming a particular one.
func (e *SignalError) Is(target error) bool {
	return target == ErrSignal
}

// IsSignalError reports whether err is a server stopping on a shutdown signal
// rather than failing.
//
// A typed SignalError matches directly. Anything else falls back to the
// historical wording, derived from the signal names rather than hard-coded, so
// that neither SIGINT nor SIGTERM can be mistaken for a crash. The fallback is
// still text matching and cannot be exact: an unrelated error that happens to
// embed "received signal terminated" is read as a shutdown. Producers inside
// this repo should return NewSignalError so their consumers never rely on it.
func IsSignalError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSignal) {
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, sig := range handledSignals {
		if strings.Contains(msg, signalErrorPrefix+strings.ToLower(sig.String())) {
			return true
		}
	}
	return false
}
