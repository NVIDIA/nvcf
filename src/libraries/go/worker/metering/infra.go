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
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type InfraStatus string

const (
	InfraInitializing InfraStatus = "initializing"
	InfraReady        InfraStatus = "ready"
)

type Workload string

const (
	Function Workload = "function"
	Task     Workload = "task"
)

type InfraMetering struct {
	config       *Config
	workloadType Workload
	lastEvent    time.Time
	status       InfraStatus
	ctx          context.Context
	cancel       context.CancelFunc
	lock         *sync.RWMutex
	ticker       *time.Ticker
}

type nvcfInfraEventDataProperties struct {
	InstanceType      string      `json:"instance_type"`
	InstanceId        string      `json:"instance_id"`
	Backend           string      `json:"backend"`
	FunctionId        string      `json:"function_id"`
	FunctionVersionId string      `json:"function_version_id"`
	Duration          float64     `json:"duration"`
	Status            InfraStatus `json:"status"`
	Gpus              int         `json:"gpus"`
	GpuType           string      `json:"gpu_type"`
	OwnerNcaId        string      `json:"owner_nca_id"`
	UserFunctionTags  []string    `json:"user_function_tags,omitempty"`
}

type nvctInfraEventDataProperties struct {
	InstanceType string      `json:"instance_type"`
	InstanceId   string      `json:"instance_id"`
	Backend      string      `json:"backend"`
	Duration     float64     `json:"duration"`
	Status       InfraStatus `json:"status"`
	Gpus         int         `json:"gpus"`
	GpuType      string      `json:"gpu_type"`
	OwnerNcaId   string      `json:"owner_nca_id"`
	TaskId       string      `json:"task_id"`
	UserTaskTags []string    `json:"user_task_tags,omitempty"`
}

func NewInfraMetering(ctx context.Context, workload Workload, config *Config, status InfraStatus) *InfraMetering {
	ctx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(config.InfraHeartbeatInterval)

	im := &InfraMetering{
		status:       status,
		workloadType: workload,
		config:       config,
		ctx:          ctx,
		cancel:       cancel,
		lock:         &sync.RWMutex{},
		ticker:       ticker,
		lastEvent:    config.StartupTime,
	}
	_ = im.logEvent()

	go startMeteringInfra(im)

	return im
}

func (im *InfraMetering) logEvent() error {
	im.lock.RLock()
	status := im.status
	im.lock.RUnlock()

	now := time.Now().UTC()
	duration := now.Sub(im.lastEvent)

	var enc []byte
	var err error

	switch im.workloadType {
	case Function:
		meteringEvent := event[nvcfInfraEventDataProperties]{
			Level:       "info",
			Message:     "infrastructure metering event",
			Environment: im.config.ICMSEnvironment,
			Data: eventData[nvcfInfraEventDataProperties]{
				Timestamp:     now,
				TransactionId: uuid.New(),
				NcaId:         im.config.BillingNcaId,
				NspectId:      im.config.NspectId,
				EventType:     "NVCF_Infrastructure",
				Properties: nvcfInfraEventDataProperties{
					InstanceType:      im.config.InstanceType,
					InstanceId:        im.config.InstanceId,
					Backend:           im.config.Backend,
					FunctionId:        im.config.FunctionId,
					FunctionVersionId: im.config.FunctionVersionId,
					Duration:          duration.Seconds(),
					Status:            status,
					Gpus:              im.config.GpuCount,
					GpuType:           im.config.GpuType,
					OwnerNcaId:        im.config.NcaId,
					UserFunctionTags:  im.config.FunctionTags,
				},
			},
		}
		enc, err = json.Marshal(meteringEvent)

	case Task:
		meteringEvent := event[nvctInfraEventDataProperties]{
			Level:       "info",
			Message:     "infrastructure metering event",
			Environment: im.config.ICMSEnvironment,
			Data: eventData[nvctInfraEventDataProperties]{
				Timestamp:     now,
				TransactionId: uuid.New(),
				NcaId:         im.config.BillingNcaId,
				NspectId:      im.config.NspectId,
				EventType:     "NVCT_Infrastructure",
				Properties: nvctInfraEventDataProperties{
					InstanceType: im.config.InstanceType,
					InstanceId:   im.config.InstanceId,
					Backend:      im.config.Backend,
					Duration:     duration.Seconds(),
					Status:       status,
					Gpus:         im.config.GpuCount,
					GpuType:      im.config.GpuType,
					OwnerNcaId:   im.config.NcaId,
					TaskId:       im.config.TaskId,
					UserTaskTags: im.config.TaskTags,
				},
			},
		}
		enc, err = json.Marshal(meteringEvent)

	default:
		return fmt.Errorf("invalid workload: %s", im.workloadType)
	}

	if err != nil {
		zap.L().Warn("failed to marshal metering event", zap.Error(err))
		return err
	}

	// XXX: UCP requires that metering events come in this very specific format.
	zap.L().Info(string(enc), meteringMarker)

	im.lastEvent = now
	return nil
}

func startMeteringInfra(im *InfraMetering) {
	for {
		select {
		case <-im.ctx.Done():
			im.ticker.Stop()
			_ = im.logEvent()
			return
		case <-im.ticker.C:
			_ = im.logEvent()
		}
	}
}

func (im *InfraMetering) Close() error {
	im.cancel()
	return nil
}

func (im *InfraMetering) SetStatus(status InfraStatus) {
	im.lock.Lock()
	im.status = status
	im.lock.Unlock()
}
