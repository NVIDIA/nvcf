// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !spiffeid_charset_backcompat
// +build !spiffeid_charset_backcompat

package spiffeid

func isBackcompatTrustDomainChar(c uint8) bool {
	return false
}

func isBackcompatPathChar(c uint8) bool {
	return false
}
