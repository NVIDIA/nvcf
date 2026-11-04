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

package http

import (
	"fmt"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/sirupsen/logrus"
)

type leveledLogger struct {
	logger logrus.FieldLogger
}

var _ retryablehttp.LeveledLogger = leveledLogger{}

func (l leveledLogger) Error(msg string, keysAndValues ...any) {
	l.logger.WithFields(makeFields(keysAndValues)).Error(msg)
}

func (l leveledLogger) Info(msg string, keysAndValues ...any) {
	l.logger.WithFields(makeFields(keysAndValues)).Info(msg)
}

func (l leveledLogger) Debug(msg string, keysAndValues ...any) {
	l.logger.WithFields(makeFields(keysAndValues)).Debug(msg)
}

func (l leveledLogger) Warn(msg string, keysAndValues ...any) {
	l.logger.WithFields(makeFields(keysAndValues)).Warn(msg)
}

func makeFields(kvs []any) logrus.Fields {
	f := logrus.Fields{}
	for i := 0; i < len(kvs); i += 2 {
		var v any
		if i+1 < len(kvs) {
			v = kvs[i+1]
		}
		f[fmt.Sprint(kvs[i])] = v
	}
	return f
}
