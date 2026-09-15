//go:build windows
// +build windows

package child

/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-FileCopyrightText: Copyright (c) HashiCorp, Inc.
SPDX-License-Identifier: MPL-2.0
*/

import "os/exec"

func setSysProcAttr(cmd *exec.Cmd, setpgid, setsid bool) {}

func processNotFoundErr(err error) bool {
	return false
}
