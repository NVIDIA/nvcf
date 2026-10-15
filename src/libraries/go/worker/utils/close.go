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

package utils

import (
	"io"

	"go.uber.org/zap"
)

func ClosePreservingError(errPtr *error, closer io.Closer) {
	// existing error takes precedence
	if *errPtr != nil {
		_ = closer.Close()
		return
	}
	*errPtr = closer.Close()
}

func Close(closer func() error) {
	err := closer()
	if err != nil {
		zap.L().Warn("close failed", zap.Error(err))
	}
}

type CloserFunc func() error

func (f CloserFunc) Close() error {
	return f()
}
