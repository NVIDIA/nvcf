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

package metering

import (
	"os"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

const DefaultInfraMeteringHeartbeatInterval = time.Duration(30) * time.Second

const EnvNspectId = "NVCF_NSPECT_ID"

func NspectIdFromEnv() string {
	return os.Getenv(EnvNspectId)
}

var meteringMarker = zap.Bool("metering", true)

type Config struct {
	Backend                string
	NcaId                  string
	BillingNcaId           string
	InstanceId             string
	InstanceType           string
	ICMSEnvironment        string
	ZoneName               string
	NspectId               string
	StartupTime            time.Time
	InfraHeartbeatInterval time.Duration
	GpuCount               int
	GpuType                string
	FunctionId             string
	FunctionVersionId      string
	FunctionTags           []string
	TaskId                 string
	TaskTags               []string
}

type inferenceEventDataProperties struct {
	RequestId         string            `json:"request_id"`
	Subject           string            `json:"sub"`
	InstanceType      string            `json:"instance_type"`
	Backend           string            `json:"backend"`
	FunctionId        string            `json:"function_id"`
	FunctionVersionId string            `json:"function_version_id"`
	Size              int64             `json:"size"`
	Duration          int               `json:"duration"`
	ResponseType      string            `json:"response_type"`
	Gpus              int               `json:"gpus"`
	GpuType           string            `json:"gpu_type"`
	OwnerNcaId        string            `json:"owner_nca_id"`
	UserFunctionTags  []string          `json:"user_function_tags,omitempty"`
	BillingHeaders    map[string]string `json:"billing_headers,omitempty"`
}

type eventData[T any] struct {
	Timestamp     time.Time `json:"timestamp"`
	TransactionId uuid.UUID `json:"transaction_id"`
	NcaId         string    `json:"nca_id"`
	NspectId      string    `json:"nspect_id"`
	EventType     string    `json:"event_type"`
	Properties    T         `json:"properties"`
}

type event[T any] struct {
	Level       string       `json:"level"`
	Message     string       `json:"msg"`
	Environment string       `json:"env"`
	Data        eventData[T] `json:"data"`
}

type EventDetails struct {
	config                   *Config
	RequestId                string
	Subject                  string
	NcaId                    string
	InferenceDurationSeconds int
	InferenceSize            int64
	ResponseType             string
	BillingHeaders           map[string]string
}

func New(config *Config, requestId string, subject string, ncaId string, headers []*pb.StringKV) *EventDetails {
	var billingHeaders map[string]string
	for _, header := range headers {
		key := strings.ToLower(header.Key)
		if strings.HasPrefix(key, "x-billing-") {
			if len(billingHeaders) == 0 {
				billingHeaders = make(map[string]string)
			}
			billingHeaders[key] = header.Value
		}
	}
	return &EventDetails{
		config:         config,
		RequestId:      requestId,
		Subject:        subject,
		NcaId:          ncaId,
		BillingHeaders: billingHeaders,
	}
}

func (e *EventDetails) Log() error {
	meteringEvent := event[inferenceEventDataProperties]{
		Level:       "info",
		Message:     "metering event",
		Environment: e.config.ICMSEnvironment,
		Data: eventData[inferenceEventDataProperties]{
			Timestamp:     time.Now().UTC(),
			TransactionId: uuid.New(),
			NcaId:         e.NcaId,
			NspectId:      e.config.NspectId,
			EventType:     "NVCF_Invocation",
			Properties: inferenceEventDataProperties{
				RequestId:         e.RequestId,
				Subject:           e.Subject,
				InstanceType:      e.config.InstanceType,
				Backend:           e.config.Backend,
				FunctionId:        e.config.FunctionId,
				FunctionVersionId: e.config.FunctionVersionId,
				Size:              e.InferenceSize,
				Duration:          e.InferenceDurationSeconds,
				ResponseType:      e.ResponseType,
				Gpus:              e.config.GpuCount,
				GpuType:           e.config.GpuType,
				OwnerNcaId:        e.config.NcaId,
				UserFunctionTags:  e.config.FunctionTags,
				BillingHeaders:    e.BillingHeaders,
			},
		},
	}

	enc, err := json.Marshal(meteringEvent)
	if err != nil {
		return err
	}
	// XXX: UCP requires that metering events come in this very specific format.
	zap.L().Info(string(enc), meteringMarker)
	return nil
}
