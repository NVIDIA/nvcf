// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"time"

	smithytime "github.com/aws/smithy-go/time"
)

// ParseTime parses a time string like the HTTP Date header. This uses a more
// relaxed rule set for date parsing compared to the standard library.
func ParseTime(text string) (t time.Time, err error) {
	return smithytime.ParseHTTPDate(text)
}
