// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lo

// Clonable defines a constraint of types having Clone() T method.
type Clonable[T any] interface {
	Clone() T
}
