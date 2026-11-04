// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ini

// Copy from https://github.com/aws/aws-sdk-go
// May have been modified by Beijing Volcanoengine Technology Ltd.

var commaRunes = []rune(",")

func isComma(b rune) bool {
	return b == ','
}

func newCommaToken() Token {
	return newToken(TokenComma, commaRunes, NoneType)
}
