// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cobraautobind

import (
	"errors"
	"fmt"
)

var (
	ErrConfigMustBeNonNil = errors.New("config must be a non-nil pointer to a struct")
	ErrConfigMustBeStruct = errors.New("config must be a pointer to a struct")
)

// InvalidDefaultError is returned when a default value for a field cannot be converted to the field type.
type InvalidDefaultError struct {
	fieldType       string
	fullFlag        string
	conversionError error
}

func (e *InvalidDefaultError) Error() string {
	return fmt.Sprintf("invalid default for %s flag %q: %v", e.fieldType, e.fullFlag, e.conversionError)
}

func (e *InvalidDefaultError) Unwrap() error {
	return e.conversionError
}

// UnsupportedFieldTypeError is returned when a field type is not supported by the system.
type UnsupportedFieldTypeError struct {
	fieldType string
	fullFlag  string
}

func (e *UnsupportedFieldTypeError) Error() string {
	return fmt.Sprintf("unsupported field type for flag: %s (%s)", e.fullFlag, e.fieldType)
}
