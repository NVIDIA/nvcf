// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"time"
)

type suppressedContext struct {
	context.Context
}

func (s *suppressedContext) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

func (s *suppressedContext) Done() <-chan struct{} {
	return nil
}

func (s *suppressedContext) Err() error {
	return nil
}
