// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package utilities

import (
	"bytes"
	"io"
)

// IOReaderFactory takes in an io.Reader and returns a function that will allow you to create a new reader that begins
// at the start of the stream
func IOReaderFactory(r io.Reader) (func() io.Reader, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	return func() io.Reader {
		return bytes.NewReader(b)
	}, nil
}
