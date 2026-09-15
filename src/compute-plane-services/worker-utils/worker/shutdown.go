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

package worker

import (
	"os"
	"strings"
	"syscall"
)

// signalErrorPrefix is how the nvkit servers run group formats the terminal
// error it returns when a shutdown signal arrives. Keep in sync with the
// signal actor in pkg/nvkit/servers/grpc.go, which returns
// fmt.Errorf("received signal %s", sig).
const signalErrorPrefix = "received signal "

// shutdownSignals are the signals that run group installs a handler for.
// SIGTERM is how Kubernetes asks a container to stop, so it is the signal the
// worker actually sees in production; SIGINT only shows up in local runs.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

// IsShutdownSignalError reports whether err is the run group reporting a
// graceful shutdown signal rather than a server failure.
//
// The servers package models a shutdown signal as a terminal error, so an
// expected stop and a genuine crash both reach callers as ordinary errors and
// can only be told apart by inspecting the message. Matching is derived from
// the signal names rather than one hard-coded string: comparing against the
// SIGINT wording alone classified every SIGTERM, and therefore every normal pod
// termination, as a crash.
func IsShutdownSignalError(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	for _, sig := range shutdownSignals {
		if strings.Contains(msg, signalErrorPrefix+sig.String()) {
			return true
		}
	}
	return false
}

// isFatalServerError reports whether an error returned by the worker's server
// run group is a genuine failure worth crashing the process for.
//
// A shutdown signal is never fatal. The shutdown context is still consulted so
// that a real server error raced against an in-progress shutdown stays quiet.
func (w *NVCFWorker) isFatalServerError(err error) bool {
	return err != nil && !IsShutdownSignalError(err) && w.shutdownCtx.Err() == nil
}
