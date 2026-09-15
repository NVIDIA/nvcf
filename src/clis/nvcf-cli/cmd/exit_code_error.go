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

package cmd

import (
	"errors"
	"fmt"
)

// ExitCodeError carries a non-zero exit code through cobra's error pipeline
// so RunE handlers don't need to call os.Exit directly.
type ExitCodeError struct {
	Code int
	Msg  string
	Err  error // optional wrapped underlying error
}

func (e *ExitCodeError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Msg, e.Err)
	}
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// Unwrap returns the wrapped underlying error, satisfying errors.As/errors.Is.
func (e *ExitCodeError) Unwrap() error { return e.Err }

// ExitCodeFromError returns the exit code to use for err.
// If err is (or wraps) an *ExitCodeError it returns e.Code; otherwise 1.
func ExitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var e *ExitCodeError
	if errors.As(err, &e) {
		return e.Code
	}
	return 1
}
