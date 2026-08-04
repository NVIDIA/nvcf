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
package com.nvidia.icms.inbound.rest.model;

import com.fasterxml.jackson.annotation.JsonProperty;

public enum SpotInstanceStatus {

    @JsonProperty("fulfilled")
    FULFILLED("fulfilled"),

    @JsonProperty("instance-terminated-no-capacity")
    INSTANCE_TERMINATED_NO_CAPACITY("instance-terminated-no-capacity"),

    @JsonProperty("instance-terminated-by-user")
    INSTANCE_TERMINATED_BY_USER("instance-terminated-by-user"),

    @JsonProperty("instance-terminated-by-service")
    INSTANCE_TERMINATED_BY_SERVICE("instance-terminated-by-service"),

    @JsonProperty("instance-terminated-cloud-offline")
    INSTANCE_TERMINATED_CLOUD_OFFLINE("instance-terminated-cloud-offline"),

    @JsonProperty("instance-terminated-lifetime-expired")
    INSTANCE_TERMINATED_LIFETIME_EXPIRED("instance-terminated-lifetime-expired");

    private final String instanceStatus;

    SpotInstanceStatus(String instanceStatus) {
        this.instanceStatus = instanceStatus;
    }

    @Override
    public String toString() {
        return this.instanceStatus;
    }
}
