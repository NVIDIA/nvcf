// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package metadata

// ClientInfo wraps immutable data from the client.Client structure.
type ClientInfo struct {
	ServiceName    string
	ServiceID      string
	APIVersion     string
	PartitionID    string
	Endpoint       string
	SigningName    string
	SigningRegion  string
	JSONVersion    string
	TargetPrefix   string
	ResolvedRegion string
}
