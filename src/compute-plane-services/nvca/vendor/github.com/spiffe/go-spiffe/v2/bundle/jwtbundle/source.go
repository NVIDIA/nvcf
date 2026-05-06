// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package jwtbundle

import (
	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// Source represents a source of JWT bundles keyed by trust domain.
type Source interface {
	// GetJWTBundleForTrustDomain returns the JWT bundle for the given trust
	// domain.
	GetJWTBundleForTrustDomain(trustDomain spiffeid.TrustDomain) (*Bundle, error)
}
