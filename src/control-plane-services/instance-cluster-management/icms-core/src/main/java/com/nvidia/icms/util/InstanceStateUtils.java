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
package com.nvidia.icms.util;

import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.RUNNING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.SHUTTING_DOWN;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.STARTING;
import static com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState.TERMINATED;
import static java.util.Map.entry;

import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.Set;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class InstanceStateUtils {

    private static final Map<SpotInstanceInternalState, Set<SpotInstanceInternalState>> INSTANCE_STATE_TRANSITION_MAP = Map.ofEntries(
            entry(STARTING, Set.of(STARTING, RUNNING, SHUTTING_DOWN, TERMINATED)),
            entry(RUNNING, Set.of(STARTING, RUNNING, SHUTTING_DOWN, TERMINATED)),
            entry(SHUTTING_DOWN, Set.of(SHUTTING_DOWN, TERMINATED)),
            entry(TERMINATED, Set.of(TERMINATED)));

    public static void validateInstanceState(
            @NotNull SpotInstanceInternalState actual,
            @NotNull Set<SpotInstanceInternalState> expected,
            @Nullable String instanceId,
            @Nullable String requestId) {

        if (expected.contains(actual)) {
            return;
        }
        String msg = String.format("Invalid instance state %s for updating instance status",
                                   actual.getStateName());
        log.error("requestId: {} instanceId: {} error: {}", requestId, instanceId, msg);
        throw new PreConditionFailedException(msg);
    }

    public static void validateInstanceStateTransition(
            SpotInstanceInternalState currentState, SpotInstanceInternalState newState,
            String instanceId, String requestId) {
        if (isTransitionValid(currentState, newState)) {
            return;
        }

        String msg = String.format(
                "Invalid instance state transition from %s to %s for updating instance status",
                currentState.getStateName(), newState.getStateName());
        log.error("requestId: {} instanceId: {} errMsg: {}", requestId, instanceId, msg);
        throw new PreConditionFailedException(msg);
    }

    private static boolean isTransitionValid(
            SpotInstanceInternalState currentState, SpotInstanceInternalState newState) {
        return INSTANCE_STATE_TRANSITION_MAP.containsKey(currentState)
                && INSTANCE_STATE_TRANSITION_MAP.get(currentState).contains(newState);
    }
}
