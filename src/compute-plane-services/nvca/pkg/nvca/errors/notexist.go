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
)

// NotExistError is an error that states that a particular resource does not exist.
func NotExistError(wrapped error) error {
	return &notExistError{err: wrapped}
}

func IsNotExist(err error) bool {
	nee := &notExistError{}
	return errors.As(err, &nee)
}

type notExistError struct {
	err error
}

func (nee *notExistError) Unwrap() error { return nee.err }

func (nee *notExistError) Error() string {
	if nee.err == nil {
		return "nil not exist error"
	}
	return "not exist error: " + nee.err.Error()
}

func (nee *notExistError) Is(err error) bool { return IsNotExist(err) }
