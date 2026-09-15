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

public enum SpotRequestStatusCode {

    @JsonProperty("pending-evaluation")
    PENDING_EVALUATION("pending-evaluation"),

    @JsonProperty("bad-parameters")
    BAD_PARAMETERS("bad-parameters"),

    @JsonProperty("capacity-not-available")
    CAPACITY_NOT_AVAILABLE("capacity-not-available"),

    @JsonProperty("not-scheduled-yet")
    NOT_SCHEDULED_YET("not-scheduled-yet"),

    @JsonProperty("constraint-not-fulfillable")
    CONSTRAINT_NOT_FULFILLABLE("constraint-not-fulfillable"),

    @JsonProperty("canceled-before-fulfillment")
    CANCELED_BEFORE_FULFILLMENT("canceled-before-fulfillment"),

    @JsonProperty("system-error")
    SYSTEM_ERROR("system-error"),

    @JsonProperty("pending-fulfillment")
    PENDING_FULFILLMENT("pending-fulfillment"),

    @JsonProperty("fulfilled")
    FULFILLED("fulfilled"),

    @JsonProperty("instance-terminated-no-capacity")
    INSTANCE_TERMINATED_NO_CAPACITY("instance-terminated-no-capacity"),

    @JsonProperty("instance-terminated-by-user")
    INSTANCE_TERMINATED_BY_USER("instance-terminated-by-user"),

    @JsonProperty("instance-terminated-by-service")
    INSTANCE_TERMINATED_BY_SERVICE("instance-terminated-by-service"),

    @JsonProperty("schedule-expired")
    SCHEDULE_EXPIRED("schedule-expired"),

    @JsonProperty("request-terminated-by-user")
    REQUEST_TERMINATED_BY_USER("request-terminated-by-user"),

    @JsonProperty("cannot-fulfill")
    CANNOT_FULFILL("cannot-fulfill"),

    @JsonProperty("request-terminated-by-service")
    REQUEST_TERMINATED_BY_SERVICE("request-terminated-by-service");

    private final String requestStatusCode;

    SpotRequestStatusCode(String requestStatusCode) {
        this.requestStatusCode = requestStatusCode;
    }

    @Override
    public String toString() {
        return this.requestStatusCode;
    }
}
