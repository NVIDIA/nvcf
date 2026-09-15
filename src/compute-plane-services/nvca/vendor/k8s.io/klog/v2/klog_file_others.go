// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows
// +build !windows

package klog

import (
	"os/user"
)

func getUserName() string {
	userNameOnce.Do(func() {
		current, err := user.Current()
		if err == nil {
			userName = current.Username
		}
	})

	return userName
}
