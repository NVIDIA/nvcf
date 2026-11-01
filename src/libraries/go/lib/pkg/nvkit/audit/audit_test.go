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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestNewNoopAuditor(t *testing.T) {
	auditor := NewNoopAuditor()
	assert.NotNil(t, auditor)
	auditor.RecordEvent(&AuditPayload{
		Timestamp: time.Now(),
		Operation: "test-op",
		Summary:   "test summary",
	})
}

func TestNewLogAuditor(t *testing.T) {
	auditor, err := NewLogAuditor()
	require.NoError(t, err)
	assert.NotNil(t, auditor)
	auditor.RecordEvent(&AuditPayload{
		Timestamp: time.Now(),
		Operation: "create",
		Type:      "resource",
		ActorID:   "user-123",
		Summary:   "created resource",
		Data: AuditRequestData{
			HttpMethod: "POST",
			RequestUri: "/api/v1/resource",
			HttpStatus: 201,
		},
	})
}

func TestAuditLevel_String(t *testing.T) {
	assert.Equal(t, "audit", auditLevel.String())
	assert.Equal(t, "AUDIT", auditLevel.CapitalString())
}

func TestAuditLevel_MarshalText(t *testing.T) {
	data, err := auditLevel.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, "audit", string(data))
}

func TestAuditLevel_UnmarshalText(t *testing.T) {
	var l AuditLevel
	err := (&l).UnmarshalText([]byte("audit"))
	assert.NoError(t, err)
	assert.Equal(t, "audit", l.String())
}

func TestAuditLevel_SetAndGet(t *testing.T) {
	var l AuditLevel
	err := (&l).Set("audit")
	assert.NoError(t, err)
	// Get() returns the level value; verify its string representation equals the set string.
	assert.Equal(t, "audit", l.String())
	assert.Equal(t, l, l.Get().(AuditLevel))
}

func TestAuditLevel_EnabledAtLevel0(t *testing.T) {
	// zapcore.DebugLevel == 0; AuditLevel.Enabled always returns true regardless of the level.
	const level0 = 0
	assert.True(t, auditLevel.Enabled(level0))
}

func TestNoopAuditor_RecordEventNil(t *testing.T) {
	auditor := &NoopAuditor{}
	auditor.RecordEvent(&AuditPayload{})
}

func TestLogAuditor_RecordEvent(t *testing.T) {
	auditor, err := NewLogAuditor()
	require.NoError(t, err)
	// Calling RecordEvent exercises AuditLevelEncoder via the zap logger
	auditor.RecordEvent(&AuditPayload{
		Data: AuditRequestData{
			HttpMethod: "GET",
			RequestUri: "/test",
			HttpStatus: 200,
		},
	})
}

func TestAuditLevelEncoder(t *testing.T) {
	// AuditLevelEncoder encodes levels as the audit level string.
	// Verify via the level encoder set on the logAuditor's zap config.
	enc := &testPrimitiveEncoder{}
	AuditLevelEncoder(zapcore.InfoLevel, enc)
	assert.Equal(t, []string{"AUDIT"}, enc.strings)
}

// testPrimitiveEncoder is a minimal zapcore.PrimitiveArrayEncoder for tests.
type testPrimitiveEncoder struct {
	strings []string
}

func (e *testPrimitiveEncoder) AppendBool(_ bool)             {}
func (e *testPrimitiveEncoder) AppendByteString(_ []byte)     {}
func (e *testPrimitiveEncoder) AppendComplex128(_ complex128) {}
func (e *testPrimitiveEncoder) AppendComplex64(_ complex64)   {}
func (e *testPrimitiveEncoder) AppendFloat64(_ float64)       {}
func (e *testPrimitiveEncoder) AppendFloat32(_ float32)       {}
func (e *testPrimitiveEncoder) AppendInt(_ int)               {}
func (e *testPrimitiveEncoder) AppendInt64(_ int64)           {}
func (e *testPrimitiveEncoder) AppendInt32(_ int32)           {}
func (e *testPrimitiveEncoder) AppendInt16(_ int16)           {}
func (e *testPrimitiveEncoder) AppendInt8(_ int8)             {}
func (e *testPrimitiveEncoder) AppendString(s string)         { e.strings = append(e.strings, s) }
func (e *testPrimitiveEncoder) AppendUint(_ uint)             {}
func (e *testPrimitiveEncoder) AppendUint64(_ uint64)         {}
func (e *testPrimitiveEncoder) AppendUint32(_ uint32)         {}
func (e *testPrimitiveEncoder) AppendUint16(_ uint16)         {}
func (e *testPrimitiveEncoder) AppendUint8(_ uint8)           {}
func (e *testPrimitiveEncoder) AppendUintptr(_ uintptr)       {}
