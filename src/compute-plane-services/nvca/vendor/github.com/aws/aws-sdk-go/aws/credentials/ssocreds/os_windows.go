// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ssocreds

import "os"

func getHomeDirectory() string {
	return os.Getenv("USERPROFILE")
}
