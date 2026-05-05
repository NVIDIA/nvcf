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

package nvca

import (
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// getRegistrationGPUs gets the current GPU state that should be registered with ICMS
func (a *Agent) getRegistrationGPUs(ctx context.Context) ([]types.RegistrationGPU, error) {
	log := core.GetLogger(ctx)

	// Wait for node cache to populate
	a.backendk8scache.ForceSync(ctx)

	backendGPUs, err := a.backendk8scache.GetAllBackendGPUs(ctx)
	if err != nil {
		log.WithError(err).Error("Get backend GPUs")
		return nil, fmt.Errorf("get instanceTypes for ICMS registration request: %v", err)
	}

	regBackendGPUs, err := a.backendk8scache.GetRegisteredBackendGPUs(ctx,
		backendGPUs,
		a.MultiNodeWorkloadsEnabled)
	if err != nil {
		log.WithError(err).Error("Get registered backend GPUs")
		return nil, fmt.Errorf("get registered backend GPUs for ICMS registration request: %v", err)
	}

	return regBackendGPUs, nil
}

// doRegistrationUpdate performs the actual ICMS registration update
func (a *Agent) doRegistrationUpdate(ctx context.Context, regBackendGPUs []types.RegistrationGPU, reason string) error {
	log := core.GetLogger(ctx)

	log.Info(reason)
	if _, err := a.registerWithICMS(ctx, regBackendGPUs); err != nil {
		return err
	}
	log.Info("Successfully updated ICMS registration")
	return nil
}

func (a *Agent) initICMSRegistrationSyncer(ctx context.Context) error {
	log := core.GetLogger(ctx)

	if !a.DynamicGPUDiscoveryEnabled {
		// Static GPU mode - only hourly refresh
		log.Info("Dynamic GPU discovery disabled, will only perform hourly ICMS registration refresh")
		var lastRegTime = core.GetCurrentTime(ctx)

		a.syncICMSRegistration = func(ctx context.Context) error {
			if timeSinceLastUpdate := core.GetCurrentTime(ctx).Sub(lastRegTime); timeSinceLastUpdate < time.Hour {
				return nil
			}

			regBackendGPUs, err := a.getRegistrationGPUs(ctx)
			if err != nil {
				return err
			}

			if err := a.doRegistrationUpdate(ctx, regBackendGPUs, "Performing hourly ICMS registration refresh"); err != nil {
				return err
			}

			lastRegTime = core.GetCurrentTime(ctx)
			return nil
		}
		return nil
	}

	// Dynamic GPU mode - check for changes and hourly refresh
	var lastRegBackendGPUs []types.RegistrationGPU
	var lastRegTime = core.GetCurrentTime(ctx)

	a.syncICMSRegistration = func(ctx context.Context) error {
		regBackendGPUs, err := a.getRegistrationGPUs(ctx)
		if err != nil {
			return err
		}

		// Determine if we need to register
		gpusChanged := len(lastRegBackendGPUs) == 0 || !cmp.Equal(lastRegBackendGPUs, regBackendGPUs, cmpopts.EquateEmpty())
		timeForHourlyRefresh := core.GetCurrentTime(ctx).Sub(lastRegTime) >= time.Hour

		if !gpusChanged && !timeForHourlyRefresh {
			log.Debug("Backend GPUs are up to date")
			return nil
		}

		// Register with appropriate reason
		var reason string
		if timeForHourlyRefresh {
			reason = "Performing hourly ICMS registration refresh"
		} else {
			reason = "Registering with ICMS due to GPU changes"
		}

		if err := a.doRegistrationUpdate(ctx, regBackendGPUs, reason); err != nil {
			return err
		}

		lastRegBackendGPUs = regBackendGPUs
		lastRegTime = core.GetCurrentTime(ctx)
		return nil
	}

	return nil
}
