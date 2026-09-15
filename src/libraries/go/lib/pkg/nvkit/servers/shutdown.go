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

package servers

import (
	"os"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/shutdown"
)

// awaitShutdownSignal blocks until a shutdown signal arrives or the run group
// tears this actor down, and is the run group's signal actor.
//
// A signal is reported as a *shutdown.SignalError. The run group has no way to
// end an actor other than returning an error, so a graceful stop and a crash
// reach the caller through the same channel; the typed error is what lets them
// be told apart with errors.Is instead of by matching the message.
//
// cancelInterrupt closing means another actor already failed and the group is
// shutting this one down, which is not this actor's error to report.
func awaitShutdownSignal(signals <-chan os.Signal, cancelInterrupt <-chan struct{}) error {
	select {
	case sig := <-signals:
		return shutdown.NewSignalError(sig)
	case <-cancelInterrupt:
		return nil
	}
}
