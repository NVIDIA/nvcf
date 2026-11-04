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
import jakarta.validation.constraints.NotNull;
import java.util.List;

/*
  started->running->shutting-down->terminated
  started->failed->terminated
  started->running->failed->terminated
 */
public enum SpotInstanceInternalState {

    @JsonProperty("starting")
    STARTING("starting"),

    @JsonProperty("running")
    RUNNING("running"),

    @JsonProperty("terminated")
    TERMINATED("terminated"),

    @JsonProperty("shutting-down")
    SHUTTING_DOWN("shutting-down");

    private final String state;

    public static final List<SpotInstanceInternalState> activeInstanceStateList = List.of(
            SpotInstanceInternalState.STARTING,
            SpotInstanceInternalState.RUNNING,
            SpotInstanceInternalState.SHUTTING_DOWN
    );

    SpotInstanceInternalState(String state) {
        this.state = state;
    }

    public String getStateName() {
        return this.state;
    }

    public static int getStateCode(@NotNull SpotInstanceInternalState state) {
        return switch (state) {
            case RUNNING -> 16;
            case SHUTTING_DOWN -> 32;
            case STARTING -> 0;
            case TERMINATED -> 48;
            default -> -1;
        };
    }
}
