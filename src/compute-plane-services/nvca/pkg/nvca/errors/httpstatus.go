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
	"net/http"
)

// HTTPStatusError creates a new HTTPStatusError with the given status code and optional wrapped error
func HTTPStatusError(code int, err error) error {
	return &httpStatusError{
		code: code,
		err:  err,
	}
}

func IsHTTPStatus(err error) bool {
	he := &httpStatusError{}
	return errors.As(err, &he)
}

type httpStatusError struct {
	code int
	err  error
}

func (he *httpStatusError) Code() int { return he.code }

func (he *httpStatusError) Unwrap() error { return he.err }

func (he *httpStatusError) Error() string {
	if he.err == nil {
		return fmt.Sprintf("HTTP %d: %s", he.code, http.StatusText(he.code))
	}
	return fmt.Sprintf("HTTP %d: %s", he.code, he.err.Error())
}

func (he *httpStatusError) Is(err error) bool { return IsHTTPStatus(err) }

// GetHTTPStatusCode returns the HTTP status code if the error is an HTTPStatusError, otherwise returns 0
func GetHTTPStatusCode(err error) int {
	he := &httpStatusError{}
	if errors.As(err, &he) {
		return he.Code()
	}
	return 0
}
