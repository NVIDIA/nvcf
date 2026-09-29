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

package codex

import (
	"context"
	"fmt"
	"slices"

	"github.com/linkedin/goavro/v2"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"
)

type Codex struct {
	logger               *otelzap.Logger
	envelope             *goavro.Codec
	stageTransitionEvent *goavro.Codec
	validMsgTypes        []string
}

func NewCodex(logger *otelzap.Logger) *Codex {
	validMsgTypes := []string{
		"stage_transition_event",
	}

	return &Codex{
		logger:               logger,
		envelope:             CreateEnvelopeCodec(logger),
		stageTransitionEvent: CreateAvroStageTransitionEventCodec(logger),
		validMsgTypes:        validMsgTypes,
	}
}

// TODO: Create a wrap/unwrap method on the Codex
// TODO: Move PublishMsg to use envelope
const envelopeSchema = `{
	"type": "record",
	"name": "envelope",
	"fields": [
		{"name": "MsgType", "type": "string"},
		{"name": "BinaryMessage", "type": "bytes"}
	]
}`

type Envelope struct {
	MsgType       string
	BinaryMessage []byte
}

func NewEnvelope(msgType string, binaryMessage []byte) Envelope {
	return Envelope{
		MsgType:       msgType,
		BinaryMessage: binaryMessage,
	}
}

var ErrEnvelope = Envelope{
	MsgType:       "error",
	BinaryMessage: []byte("error"),
}

func CreateEnvelopeCodec(logger *otelzap.Logger) *goavro.Codec {
	codec, err := goavro.NewCodec(envelopeSchema)
	if err != nil {
		logger.Fatal(fmt.Sprintf("Error creating Avro codec: %v", err))
	}
	return codec
}

func (c *Codex) Unwrap(traceCtx context.Context, message []byte) (Envelope, error) {
	// Tracing is now handled by the external library

	decodedMsg, _, err := c.envelope.NativeFromBinary(message)
	if err != nil {
		c.logger.ErrorContext(traceCtx, "Error decoding envelope message", zap.Error(err))
		return ErrEnvelope, err
	}

	if decodedMap, ok := decodedMsg.(map[string]interface{}); ok {
		msgType := decodedMap["MsgType"].(string)
		binaryMessage := decodedMap["BinaryMessage"].([]byte)
		if !slices.Contains(c.validMsgTypes, msgType) {
			c.logger.ErrorContext(traceCtx, "Invalid envelope message type", zap.String("msgType", msgType))
			return ErrEnvelope, fmt.Errorf("invalid envelope message type")
		}
		envelope := NewEnvelope(msgType, binaryMessage)
		return envelope, nil
	}
	return ErrEnvelope, fmt.Errorf("invalid envelope message")
}

func (c *Codex) Wrap(traceCtx context.Context, msgType string, body []byte) ([]byte, error) {
	// Tracing is now handled by the external library

	if !slices.Contains(c.validMsgTypes, msgType) {
		return nil, fmt.Errorf("invalid envelope message type: %s", msgType)
	}

	msg := map[string]interface{}{
		"MsgType":       msgType,
		"BinaryMessage": body,
	}

	binaryMsg, err := c.envelope.BinaryFromNative(nil, msg)
	if err != nil {
		c.logger.ErrorContext(traceCtx, "Error converting envelope to binary", zap.Error(err))
		return nil, err
	}

	return binaryMsg, nil
}
