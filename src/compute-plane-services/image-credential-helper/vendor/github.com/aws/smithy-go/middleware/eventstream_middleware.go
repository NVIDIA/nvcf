// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package middleware

type eventStreamOutputKey struct{}

func AddEventStreamOutputToMetadata(metadata *Metadata, output any) {
	metadata.Set(eventStreamOutputKey{}, output)
}

func GetEventStreamOutputToMetadata[T any](metadata *Metadata) (*T, bool) {
	val := metadata.Get(eventStreamOutputKey{})
	// not found
	if val == nil {
		return nil, false
	}
	// wrong type
	res, ok := val.(*T)
	if !ok {
		return nil, false
	}
	return res, true
}
