// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package httpmock

import (
	"os"
)

var envVarName = "GONOMOCKS"

// Disabled allows to test whether httpmock is enabled or not. It
// depends on GONOMOCKS environment variable.
func Disabled() bool {
	return os.Getenv(envVarName) != ""
}
