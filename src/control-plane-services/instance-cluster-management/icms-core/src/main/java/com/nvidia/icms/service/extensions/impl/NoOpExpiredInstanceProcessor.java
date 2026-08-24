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

import com.nvidia.icms.service.extensions.api.ExpiredInstanceProcessor;

import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import java.util.List;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link ExpiredInstanceProcessor} that is registered only
 * when no other {@link ExpiredInstanceProcessor} bean is present in the application
 * context.
 *
 * <p>All methods perform no action, ensuring that expired-instance cleanup does not fail
 * when non-BYOC-specific termination is not configured.
 */
@Slf4j
public class NoOpExpiredInstanceProcessor implements ExpiredInstanceProcessor {

    /**
     * In normal implementations, persists the terminal state of each expired instance and
     * dispatches the corresponding termination messages to the non-BYOC provider.
     * This no-op implementation performs no action.
     */
    @Override
    public void persistAndTerminate(List<InstanceV2Entity> expiredInstances) {
        log.debug("NoOpExpiredInstanceProcessor.persistAndTerminate called — no-op");
    }
}
