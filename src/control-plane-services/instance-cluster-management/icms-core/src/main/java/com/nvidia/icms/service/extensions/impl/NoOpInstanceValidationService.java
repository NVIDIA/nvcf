/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.icms.service.extensions.impl;

import com.nvidia.icms.service.extensions.api.InstanceValidationService;

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link InstanceValidationService} that is registered only when no other
 * {@link InstanceValidationService} bean is present in the application context.
 *
 * <p>All methods perform no validation and return safe neutral values, ensuring that calling
 * code continues without error when non-BYOC-specific validation is not configured.
 */
@Slf4j
public class NoOpInstanceValidationService implements InstanceValidationService {

    /**
     * In normal implementations, validates required fields for NVCT task requests and throws
     * {@link com.nvidia.icms.errors.IcmsBadRequestException} if a required field is absent.
     * This no-op implementation performs no action.
     */
    @Override
    public void validationForNvct(SpotInstanceRequestSchema instanceRequest) {
        log.debug("NoOpInstanceValidationService.validationForNvct called — no-op");
    }

    /**
     * In normal implementations, validates that the {@code maxRuntimeDuration} of a task
     * request does not exceed the non-BYOC-allowed maximum and throws
     * {@link com.nvidia.icms.errors.IcmsBadRequestException} if it does.
     * This no-op implementation performs no action.
     */
    @Override
    public void validateMaxRuntimeDuration(SpotInstanceRequestSchema instanceRequest) {
        log.debug("NoOpInstanceValidationService.validateMaxRuntimeDurationForNonByoc called — no-op");
    }

    /**
     * In normal implementations, returns {@code true} if the request's
     * {@code maxRuntimeDuration} is present and within the configured non-BYOC limit.
     * This no-op implementation always returns {@code true} so that runtime-duration checks
     * do not block the caller when non-BYOC validation is not configured.
     *
     * @return always {@code true}
     */
    @Override
    public boolean isMaxRuntimeDurationValid(SpotInstanceRequestSchema instanceRequest) {
        log.debug("NoOpInstanceValidationService.isMaxRuntimeDurationValidForNonByoc called — returning true");
        return true;
    }
}
