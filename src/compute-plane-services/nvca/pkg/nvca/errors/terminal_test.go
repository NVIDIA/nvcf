/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nvcaerrors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTerminalError(t *testing.T) {
	assert.True(t, IsTerminal(TerminalError(fmt.Errorf("terminal"))))
	assert.True(t, errors.Is(TerminalError(fmt.Errorf("terminal")), &terminalError{}))
	assert.False(t, IsTerminal(fmt.Errorf("not terminal")))

	testErr := fmt.Errorf("test err")
	assert.True(t, errors.Is(TerminalError(testErr).(*terminalError).Unwrap(), testErr))

	assert.Equal(t, "nil terminal error", TerminalError(nil).Error())
	assert.Equal(t, "terminal error: test err", TerminalError(testErr).Error())
}
