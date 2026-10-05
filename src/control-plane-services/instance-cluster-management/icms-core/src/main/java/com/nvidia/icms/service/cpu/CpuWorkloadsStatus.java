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
package com.nvidia.icms.service.cpu;

import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonProperty;

/**
 * Status of CPU-only workload support. The reason is a generic code and never names clusters,
 * accounts or GPUs, because the status is readable by any cluster. Details are in ICMS logs.
 *
 * @param state the state
 * @param reason generic reason code, empty when the state is {@link CpuWorkloadsState#READY}
 */
public record CpuWorkloadsStatus(
        @JsonProperty("state") CpuWorkloadsState state,
        @JsonProperty("reason") String reason) {

    public static final String REASON_DISABLED = "DISABLED";
    public static final String REASON_COLLISION_DETECTED = "COLLISION_DETECTED";
    public static final String REASON_GUARD_PENDING = "GUARD_PENDING";

    public static final CpuWorkloadsStatus DISABLED =
            new CpuWorkloadsStatus(CpuWorkloadsState.DISABLED, REASON_DISABLED);
    public static final CpuWorkloadsStatus READY =
            new CpuWorkloadsStatus(CpuWorkloadsState.READY, "");

    public static CpuWorkloadsStatus blocked(String reason) {
        return new CpuWorkloadsStatus(CpuWorkloadsState.BLOCKED, reason);
    }

    @JsonIgnore
    public boolean isReady() {
        return state == CpuWorkloadsState.READY;
    }
}
