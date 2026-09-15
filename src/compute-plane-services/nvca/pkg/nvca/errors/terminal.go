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

package nvcaerrors

import (
	"errors"
	"fmt"
)

const (
	TerminalErrorPrefix = "terminal error:"
)

// TerminalError is an error that will not be retried but still be logged
// and recorded in metrics.
func TerminalError(wrapped error) error {
	return &terminalError{err: wrapped}
}

func IsTerminal(err error) bool {
	tp := &terminalError{}
	return errors.As(err, &tp)
}

type terminalError struct {
	err error
}

func (te *terminalError) Unwrap() error { return te.err }

func (te *terminalError) Error() string {
	if te.err == nil {
		return "nil terminal error"
	}
	return fmt.Sprintf("%s %s", TerminalErrorPrefix, te.err.Error())
}

func (te *terminalError) Is(err error) bool { return IsTerminal(err) }
