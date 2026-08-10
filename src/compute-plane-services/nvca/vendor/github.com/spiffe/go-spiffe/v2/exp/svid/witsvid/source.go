// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package witsvid

import "github.com/spiffe/go-spiffe/v2/spiffeid"

// Source is a source of WIT-SVIDs keyed by SPIFFE ID.
type Source interface {
	// GetWITSVIDForID returns the WIT-SVID for the given SPIFFE ID.
	GetWITSVIDForID(id spiffeid.ID) (*SVID, error)
}
