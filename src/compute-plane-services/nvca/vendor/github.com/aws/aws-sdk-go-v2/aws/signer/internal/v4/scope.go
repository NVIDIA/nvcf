// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package v4

import "strings"

// BuildCredentialScope builds the Signature Version 4 (SigV4) signing scope
func BuildCredentialScope(signingTime SigningTime, region, service string) string {
	return strings.Join([]string{
		signingTime.ShortTimeFormat(),
		region,
		service,
		"aws4_request",
	}, "/")
}
