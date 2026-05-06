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

package progress

import "context"

// EventSink is implemented by exactly one renderer per up invocation.
//
// Implementations MUST deliver terminal events (phase_failed, phase_cancelled,
// final) synchronously. They MAY drop phase_progress events under load and
// expose a count of dropped events in the final event for telemetry.
type EventSink interface {
	Emit(ctx context.Context, e Event) error
	Close() error
}

// isTerminal returns true for events whose delivery cannot be skipped without
// breaking the orchestrator's correctness contract.
func isTerminal(e Event) bool {
	switch e.(type) {
	case PhaseFailed, PhaseCancelled, Final:
		return true
	default:
		return false
	}
}
