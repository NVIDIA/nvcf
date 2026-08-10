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

import com.nvidia.icms.service.extensions.api.HeartbeatRecorder;

import com.nvidia.icms.inbound.rest.model.cluster.ClusterHeartbeatRequest;
import lombok.extern.slf4j.Slf4j;

/**
 * No-op implementation of {@link HeartbeatRecorder} that is registered only when no other
 * {@link HeartbeatRecorder} bean is present in the application context.
 *
 * <p>All methods perform no action, ensuring that heartbeat recording does not fail when
 * no managed cluster health tracking is configured.
 */
@Slf4j
public class NoOpHeartbeatRecorder implements HeartbeatRecorder {

    /**
     * In normal implementations, records a heartbeat for the given cluster zone.
     * This no-op implementation performs no action and returns {@code null}, the only
     * valid value for the {@link Void} return type.
     *
     * @return always {@code null}
     */
    @Override
    public Void recordClusterHeartbeat(String zoneName,
                                       ClusterHeartbeatRequest heartbeatRequest) {
        log.debug("NoOpHeartbeatRecorder.recordClusterHeartbeat called — no-op");
        return null;
    }
}
