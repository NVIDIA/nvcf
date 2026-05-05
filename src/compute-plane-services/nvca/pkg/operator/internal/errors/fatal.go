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

package nvcaoperatorerrors

import (
	"errors"
)

// FatalError is an error that will not be retried but still be logged
// and recorded in metrics.
func FatalError(wrapped error) error {
	return &fatalError{err: wrapped}
}

func IsFatal(err error) bool {
	tp := &fatalError{}
	return errors.As(err, &tp)
}

type fatalError struct {
	err error
}

func (fe *fatalError) Unwrap() error { return fe.err }

func (fe *fatalError) Error() string {
	if fe.err == nil {
		return "nil fatal error"
	}
	return "fatal error: " + fe.err.Error()
}

func (fe *fatalError) Is(err error) bool { return IsFatal(err) }
