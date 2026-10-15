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
package com.nvidia.icms.inbound.rest.model.instance;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.EnumSet;
import java.util.Optional;
import java.util.Set;
import java.util.stream.Collectors;

public enum InstanceRequestState {

    @JsonProperty("open")
    OPEN("open"),

    @JsonProperty("active")
    ACTIVE("active"),

    @JsonProperty("closed")
    CLOSED("closed"),

    @JsonProperty("canceled")
    CANCELED("canceled"),

    @JsonProperty("failed")
    FAILED("failed");

    private final String state;

    InstanceRequestState(String state) {
        this.state = state;
    }

    public static Set<String> getAllInstanceRequestStates() {
        return EnumSet.allOf(InstanceRequestState.class).stream()
                .map(InstanceRequestState::toString)
                .collect(Collectors.toSet());
    }

    public static Optional<InstanceRequestState> toInstanceRequestState(String state) {
        for (InstanceRequestState requestState : InstanceRequestState.values()) {
            if (requestState.toString().equals(state)) {
                return Optional.of(requestState);
            }
        }

        return Optional.empty();
    }

    @Override
    public String toString() {
        return state.toLowerCase();
    }
}
