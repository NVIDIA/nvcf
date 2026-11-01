// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build go1.20

package errors

import "errors"

func Join(errs ...error) error {
	return errors.Join(errs...)
}
