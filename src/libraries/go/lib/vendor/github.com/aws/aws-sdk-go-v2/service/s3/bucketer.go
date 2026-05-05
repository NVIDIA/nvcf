// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package s3

// implemented by all S3 input structures
type bucketer interface {
	bucket() (string, bool)
}

func bucketFromInput(params interface{}) (string, bool) {
	v, ok := params.(bucketer)
	if !ok {
		return "", false
	}

	return v.bucket()
}
