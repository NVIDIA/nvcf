// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package errors

import "errors"

func New(text string) error {
	return errors.New(text)
}

func As(err error, target interface{}) bool {
	return errors.As(err, target)
}
