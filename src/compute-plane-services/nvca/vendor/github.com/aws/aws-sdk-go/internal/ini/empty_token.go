// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ini

// emptyToken is used to satisfy the Token interface
var emptyToken = newToken(TokenNone, []rune{}, NoneType)
