// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package jlexer

import "fmt"

// LexerError implements the error interface and represents all possible errors that can be
// generated during parsing the JSON data.
type LexerError struct {
	Reason string
	Offset int
	Data   string
}

func (l *LexerError) Error() string {
	return fmt.Sprintf("parse error: %s near offset %d of '%s'", l.Reason, l.Offset, l.Data)
}
