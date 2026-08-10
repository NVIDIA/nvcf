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

package types

import (
	"encoding/json"
	"errors"
	"fmt"
)

type WorkerError struct {
	err        error
	isInternal bool
}

func (e *WorkerError) Error() string {
	if e.err == nil {
		return ""
	}

	if e.isInternal {
		return fmt.Sprintf("internal error: %s", e.err.Error())
	}

	return fmt.Sprintf("user actionable error: %s", e.err.Error())

}

func (e *WorkerError) Unwrap() error {
	return e.err
}

func (e *WorkerError) Wrap(err error) *WorkerError {
	return &WorkerError{
		err:        fmt.Errorf("%w: %w", e.err, err),
		isInternal: e.isInternal,
	}
}

func (e *WorkerError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Message    string `json:"message"`
		IsInternal bool   `json:"isInternal"`
	}{
		Message:    e.err.Error(),
		IsInternal: e.isInternal,
	})
}

func (e *WorkerError) UnmarshalJSON(data []byte) error {
	var input struct {
		Message    string `json:"message"`
		IsInternal bool   `json:"isInternal"`
	}

	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}

	e.isInternal = input.IsInternal
	e.err = errors.New(input.Message)

	return nil
}

func NewInternalError(err error) *WorkerError {
	return &WorkerError{
		err:        err,
		isInternal: true,
	}
}

func NewUserActionableError(err error) *WorkerError {
	return &WorkerError{
		err:        err,
		isInternal: false,
	}
}
