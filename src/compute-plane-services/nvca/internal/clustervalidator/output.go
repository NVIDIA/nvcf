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
	"fmt"

	"github.com/sirupsen/logrus"
)

const (
	iconCheck = "✓"
	iconCross = "✗"
	iconWarn  = "⚠"
	iconInfo  = "ℹ"

	separator = "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
)

// CLIFormatter is a logrus formatter optimised for human-readable CLI output.
//
// Warn and Error levels are automatically wrapped in yellow / red.
// Info level is emitted verbatim so that callers can embed their own
// ANSI codes (blue for headers, green for success, none for neutral text).
type CLIFormatter struct{}

// Format implements logrus.Formatter.
func (f *CLIFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	switch entry.Level {
	case logrus.WarnLevel:
		return []byte(fmt.Sprintf("%s%s%s\n", colorYellow, entry.Message, colorReset)), nil
	case logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel:
		return []byte(fmt.Sprintf("%s%s%s\n", colorRed, entry.Message, colorReset)), nil
	default:
		return []byte(entry.Message + "\n"), nil
	}
}

func printHeader(log *logrus.Entry, title string) {
	log.Info("")
	log.Infof("%s%s%s", colorBlue, separator, colorReset)
	log.Infof("%s  %s%s", colorBlue, title, colorReset)
	log.Infof("%s%s%s", colorBlue, separator, colorReset)
}

func printSuccess(log *logrus.Entry, msg string) {
	log.Infof("%s%s  %s%s", colorGreen, iconCheck, msg, colorReset)
}

func printError(log *logrus.Entry, msg string) {
	log.Errorf("%s  %s", iconCross, msg)
}

func printWarning(log *logrus.Entry, msg string) {
	log.Warnf("%s  %s", iconWarn, msg)
}

func printInfo(log *logrus.Entry, msg string) {
	log.Infof("%s  %s", iconInfo, msg)
}

func printBlue(log *logrus.Entry, msg string) {
	log.Infof("%s%s  %s%s", colorBlue, iconInfo, msg, colorReset)
}
