// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v4

// WithUnsignedPayload will enable and set the UnsignedPayload field to
// true of the signer.
func WithUnsignedPayload(v4 *Signer) {
	v4.UnsignedPayload = true
}
