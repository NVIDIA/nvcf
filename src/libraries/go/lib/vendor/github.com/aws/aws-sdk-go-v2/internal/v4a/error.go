// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v4a

import "fmt"

// SigningError indicates an error condition occurred while performing SigV4a signing
type SigningError struct {
	Err error
}

func (e *SigningError) Error() string {
	return fmt.Sprintf("failed to sign request: %v", e.Err)
}

// Unwrap returns the underlying error cause
func (e *SigningError) Unwrap() error {
	return e.Err
}
