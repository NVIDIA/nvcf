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
package com.nvidia.icms.service.extensions.api;

import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;

/**
 * Validates non-BYOC-specific and NVCT task request fields before instance creation.
 */
public interface InstanceValidationService {

    /**
     * Validates required fields for NVCT task requests (those with a non-blank taskId).
     * No-op for non-task (function) requests.
     *
     * @param instanceRequest the incoming instance request
     * @throws com.nvidia.icms.errors.IcmsBadRequestException if a required field is absent
     */
    void validationForNvct(SpotInstanceRequestSchema instanceRequest);

    /**
     * Validates that the {@code maxRuntimeDuration} of a task request does not exceed the
     * backend-specific limit defined in configuration.
     * No-op for non-task (function) requests.
     *
     * @param instanceRequest the incoming instance request
     * @throws com.nvidia.icms.errors.IcmsBadRequestException if the duration exceeds the limit
     */
    void validateMaxRuntimeDuration(SpotInstanceRequestSchema instanceRequest);

    /**
     * Returns {@code true} if the request has a {@code maxRuntimeDuration} that is present and
     * strictly less than the backend-specific limit defined in configuration.
     *
     * @param instanceRequest the instance request to check
     * @return {@code true} if the duration is present and within the configured backend limit
     */
    boolean isMaxRuntimeDurationValid(SpotInstanceRequestSchema instanceRequest);
}
