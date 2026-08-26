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

package harness

import "context"

type recordingRunner struct {
	runs       []string
	nextResult Result
	nextErr    error
}

func (r *recordingRunner) Run(_ context.Context, command string) (Result, error) {
	r.runs = append(r.runs, command)
	return r.nextResult, r.nextErr
}

func (r *recordingRunner) RunWithSensitiveStdin(
	ctx context.Context,
	command,
	_ string,
) (Result, error) {
	return r.Run(ctx, command)
}

func (r *recordingRunner) RunWithTTY(ctx context.Context, command string) (Result, error) {
	return r.Run(ctx, command)
}
