/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package clustervalidator

import (
	"testing"
)

func TestPrintHeader(t *testing.T) {
	printHeader(testLog(), "Test Header")
}

func TestPrintSuccess(t *testing.T) {
	printSuccess(testLog(), "success message")
}

func TestPrintError(t *testing.T) {
	printError(testLog(), "error message")
}

func TestPrintWarning(t *testing.T) {
	printWarning(testLog(), "warning message")
}

func TestPrintInfo(t *testing.T) {
	printInfo(testLog(), "info message")
}
