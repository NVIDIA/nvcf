// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import "context"

// None implements a no-op rate limiter which effectively disables client-side
// rate limiting (also known as "retry quotas").
//
// GetToken does nothing and always returns a nil error. The returned
// token-release function does nothing, and always returns a nil error.
//
// AddTokens does nothing and always returns a nil error.
var None = &none{}

type none struct{}

func (*none) GetToken(ctx context.Context, cost uint) (func() error, error) {
	return func() error { return nil }, nil
}

func (*none) AddTokens(v uint) error { return nil }
