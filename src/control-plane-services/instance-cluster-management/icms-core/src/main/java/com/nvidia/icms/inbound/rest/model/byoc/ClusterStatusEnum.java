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
package com.nvidia.icms.inbound.rest.model.byoc;

import com.fasterxml.jackson.annotation.JsonProperty;
import io.swagger.v3.oas.annotations.media.Schema;

@Schema(description = "Cluster Status")
public enum ClusterStatusEnum {

    @JsonProperty("READY")
    READY("READY"),

    @JsonProperty("PAUSED")
    PAUSED("PAUSED"),

    @JsonProperty("FAILED")
    FAILED("FAILED"),

    @JsonProperty("ABANDONED")
    ABANDONED("ABANDONED"),

    @JsonProperty("NOT_READY")
    NOT_READY("NOT_READY"),

    @JsonProperty("UNHEALTHY")
    UNHEALTHY("UNHEALTHY"),

    @JsonProperty("DELETED")
    DELETED("DELETED"),

    // This status will be sent by NVCA in case of "Maintenance"
    @JsonProperty("CORDON")
    CORDON("CORDON"),

    // This status will be sent by NVCA in case of "Maintenance"
    @JsonProperty("CORDON_AND_DRAIN")
    CORDON_AND_DRAIN("CORDON_AND_DRAIN")

    ;

    private final String bartRegistrationStatus;

    ClusterStatusEnum(String bartRegistrationStatus) {
        this.bartRegistrationStatus = bartRegistrationStatus;
    }

    @Override
    public String toString() {
        return this.bartRegistrationStatus;
    }
}
