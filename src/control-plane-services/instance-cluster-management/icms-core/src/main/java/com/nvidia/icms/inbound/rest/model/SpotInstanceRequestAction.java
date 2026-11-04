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
import java.util.Optional;

public enum SpotInstanceRequestAction {

    @JsonProperty("CancelSpotInstanceRequests")
    CANCEL_SPOT_INSTANCE_REQUESTS("CancelSpotInstanceRequests"),

    @JsonProperty("DescribeInstances")
    DESCRIBE_INSTANCES("DescribeInstances"),

    @JsonProperty("DescribeSpotInstanceRequests")
    DESCRIBE_SPOT_INSTANCE_REQUESTS("DescribeSpotInstanceRequests"),

    @JsonProperty("RequestSpotInstances")
    REQUEST_SPOT_INSTANCES("RequestSpotInstances"),

    @JsonProperty("RequestSpotInstancesForTask")
    REQUEST_SPOT_INSTANCES_FOR_TASK("RequestSpotInstancesForTask"),

    @JsonProperty("TerminateInstances")
    TERMINATE_INSTANCES("TerminateInstances"),

    @JsonProperty("TerminateSpotInstanceRequest")
    TERMINATE_SPOT_INSTANCE_REQUEST("TerminateSpotInstanceRequest"),

    @JsonProperty("CancelInstanceRequests")
    CANCEL_INSTANCE_REQUESTS("CancelInstanceRequests"),

    @JsonProperty("DescribeInstanceRequests")
    DESCRIBE_INSTANCE_REQUESTS("DescribeInstanceRequests"),

    @JsonProperty("RequestInstances")
    REQUEST_INSTANCES("RequestInstances"),

    @JsonProperty("RequestInstancesForTask")
    REQUEST_INSTANCES_FOR_TASK("RequestInstancesForTask"),

    @JsonProperty("TerminateInstanceRequest")
    TERMINATE_INSTANCE_REQUEST("TerminateInstanceRequest");

    private final String requestAction;

    SpotInstanceRequestAction(String requestAction) {
        this.requestAction = requestAction;
    }

    public String getRequestAction() {
        return requestAction;
    }

    public static Optional<SpotInstanceRequestAction> toSpotInstanceRequestAction(String action) {
        for (SpotInstanceRequestAction requestAction : SpotInstanceRequestAction.values()) {
            if (requestAction.getRequestAction().equals(action)) {
                return Optional.of(requestAction);
            }
        }

        return Optional.empty();
    }

    /**
     * Translates the two new prefix-less *creation* action names
     * (RequestInstances, RequestInstancesForTask) to their legacy
     * "Spot"-prefixed counterparts. 
     */
    public SpotInstanceRequestAction toLegacyAction() {
        switch (this) {
            case REQUEST_INSTANCES:            return REQUEST_SPOT_INSTANCES;
            case REQUEST_INSTANCES_FOR_TASK:   return REQUEST_SPOT_INSTANCES_FOR_TASK;
            default:                           return this;
        }
    }
}
