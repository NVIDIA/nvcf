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

package utils

import (
	"os"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/mattn/go-isatty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var PublicLogMarker = zap.Bool("public", true)

// LevelFromEnv reads from env LOG_LEVEL and defaults to INFO
func LevelFromEnv() zap.AtomicLevel {
	level, err := zap.ParseAtomicLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return zap.NewAtomicLevelAt(zap.InfoLevel)
	}
	return level
}

func NewProductionLogger() *logs.ZapLogger {
	logger := logs.NewZapLogger(LevelFromEnv(), logs.WithAsyncFlush(100*time.Millisecond), logs.WithEncoderFunc(func(cfg zapcore.EncoderConfig) zapcore.Encoder {
		if isatty.IsTerminal(os.Stdout.Fd()) {
			return zapcore.NewConsoleEncoder(cfg)
		}
		return zapcore.NewJSONEncoder(cfg)
	}))
	zap.RedirectStdLog(logger.GetZapLogger())
	return logger
}
