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

package clients

import (
	"github.com/hashicorp/go-retryablehttp"
	"go.uber.org/zap"
)

// retryLogger implements the leveledLogger interface in the retry_http package, and provide logging functionality by
// the global zap logger. Thus, it can be used by retryable http client
type retryLogger struct{}

func (l *retryLogger) Error(msg string, keysAndValues ...interface{}) {
	zap.S().Errorw(msg, keysAndValues...)
}

func (l *retryLogger) Info(msg string, keysAndValues ...interface{}) {
	zap.S().Infow(msg, keysAndValues...)
}

func (l *retryLogger) Debug(msg string, keysAndValues ...interface{}) {
	zap.S().Debugw(msg, keysAndValues...)
}

func (l *retryLogger) Warn(msg string, keysAndValues ...interface{}) {
	zap.S().Warnw(msg, keysAndValues...)
}

// NewRetryLogger returns a new retry logger
func newRetryLogger() retryablehttp.LeveledLogger {
	return &retryLogger{}
}
