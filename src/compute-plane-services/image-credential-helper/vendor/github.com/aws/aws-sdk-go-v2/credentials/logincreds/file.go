// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package logincreds

import (
	"io"
	"os"
)

var openFile func(string) (io.ReadCloser, error) = func(name string) (io.ReadCloser, error) {
	return os.Open(name)
}

var createFile func(string) (io.WriteCloser, error) = func(name string) (io.WriteCloser, error) {
	return os.Create(name)
}
