// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package customizations

type s3DisableExpressAuthProvider interface {
	GetS3DisableExpressAuth() (bool, bool)
}

// ResolveDisableExpressAuth pulls S3DisableExpressAuth setting from config
// sources.
func ResolveDisableExpressAuth(configs []interface{}) (value bool, exists bool) {
	for _, cfg := range configs {
		if p, ok := cfg.(s3DisableExpressAuthProvider); ok {
			if value, exists = p.GetS3DisableExpressAuth(); exists {
				break
			}
		}
	}
	return
}
