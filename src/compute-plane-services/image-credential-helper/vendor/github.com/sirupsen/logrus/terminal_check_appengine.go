// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// +build appengine

package logrus

import (
	"io"
)

func checkIfTerminal(w io.Writer) bool {
	return true
}
