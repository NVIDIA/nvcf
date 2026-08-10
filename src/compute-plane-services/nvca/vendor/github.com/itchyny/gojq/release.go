// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !gojq_debug

package gojq

type codeinfo struct{}

func (*compiler) appendCodeInfo(any) {}

func (*compiler) deleteCodeInfo(string) {}

func (*env) debugCodes() {}

func (*env) debugState(int, bool) {}

func (*env) debugForks(int, string) {}
