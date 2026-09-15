// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package editiondefaults contains the binary representation of the editions
// defaults.
package editiondefaults

import _ "embed"

//go:embed editions_defaults.binpb
var Defaults []byte
