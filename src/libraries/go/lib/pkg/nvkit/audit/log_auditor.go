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

package audit

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type AuditLevel zapcore.Level

func (l AuditLevel) String() string                   { return "audit" }
func (l AuditLevel) CapitalString() string            { return "AUDIT" }
func (l AuditLevel) MarshalText() ([]byte, error)     { return []byte(l.String()), nil }
func (l *AuditLevel) UnmarshalText(text []byte) error { return nil }
func (l *AuditLevel) Set(s string) error              { return l.UnmarshalText([]byte(s)) }
func (l *AuditLevel) Get() interface{}                { return *l }
func (l AuditLevel) Enabled(_ zapcore.Level) bool     { return true }

const auditLevel AuditLevel = AuditLevel(zapcore.FatalLevel + 1)

func AuditLevelEncoder(lvl zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(auditLevel.CapitalString())
}

type logAuditor struct {
	logger *zap.Logger
}

func (l *logAuditor) RecordEvent(payload *AuditPayload) {
	l.logger.Info("AuditEvent", zap.Any("payload", payload))
}

func NewLogAuditor() (Auditor, error) {
	logSyncer := zapcore.AddSync(os.Stdout)

	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig.EncodeLevel = AuditLevelEncoder
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	cfg.EncoderConfig.EncodeDuration = zapcore.MillisDurationEncoder
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(cfg.EncoderConfig),
		logSyncer,
		zap.LevelEnablerFunc(func(lvl zapcore.Level) bool { return true }),
	)
	return &logAuditor{logger: zap.New(core)}, nil
}
