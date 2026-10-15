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

import com.nvidia.icms.outbound.cassandra.cloudhealth.entity.CloudHealthEntity;
import jakarta.validation.constraints.NotNull;

/**
 * Handles backend-specific cloud-health events.
 */
public interface CloudHealthEventService {

    /**
     * Reacts to an unhealthy backend cloud event for the given entity.
     * Implementations are expected to emit the appropriate telemetry signals.
     *
     * @param entity the cloud-health entity whose zone has become unhealthy
     */
    void handleUnhealthyCloud(@NotNull CloudHealthEntity entity);
}
