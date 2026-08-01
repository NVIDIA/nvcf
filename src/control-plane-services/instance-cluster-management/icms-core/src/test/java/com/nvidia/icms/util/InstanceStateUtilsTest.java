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
import static com.nvidia.icms.util.TestUtil.DUMMY_INSTANCE_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.icms.errors.PreConditionFailedException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import java.util.Set;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class InstanceStateUtilsTest {

    @ParameterizedTest
    @MethodSource
    void validateInstanceStateTransition_success(
            SpotInstanceInternalState currentState, SpotInstanceInternalState newState) {
        InstanceStateUtils.validateInstanceStateTransition(currentState, newState,
                                                           DUMMY_INSTANCE_ID, DUMMY_REQUEST_ID);
    }

    @ParameterizedTest
    @MethodSource
    void validateInstanceStateTransition_failure(
            SpotInstanceInternalState currentState, SpotInstanceInternalState newState) {

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                             () -> InstanceStateUtils.validateInstanceStateTransition(
                                                              currentState, newState,
                                                              DUMMY_INSTANCE_ID, DUMMY_REQUEST_ID));

        assertThat(exception.getBody().getDetail()).isEqualTo(String.format(
                "Invalid instance state transition from %s to %s for updating instance status",
                currentState.getStateName(), newState.getStateName()));
    }

    @Test
    void validateInstanceState_success() {
        InstanceStateUtils.validateInstanceState(STARTING, Set.of(STARTING, RUNNING),
                                                 DUMMY_INSTANCE_ID, DUMMY_REQUEST_ID);
    }

    @Test
    void validateInstanceState_failure() {

        PreConditionFailedException exception = assertThrows(PreConditionFailedException.class,
                                                      () -> InstanceStateUtils.validateInstanceState(
                                                              TERMINATED, Set.of(STARTING, RUNNING),
                                                              DUMMY_INSTANCE_ID, DUMMY_REQUEST_ID));

        assertThat(exception.getBody().getDetail()).isEqualTo(
                "Invalid instance state terminated for updating instance status");
    }


    private static Stream<Arguments> validateInstanceStateTransition_success() {
        return Stream.of(Arguments.of(STARTING, STARTING), Arguments.of(STARTING, RUNNING),
                         Arguments.of(STARTING, SHUTTING_DOWN), Arguments.of(STARTING, TERMINATED),
                         Arguments.of(RUNNING, STARTING), Arguments.of(RUNNING, RUNNING),
                         Arguments.of(RUNNING, SHUTTING_DOWN), Arguments.of(RUNNING, TERMINATED),
                         Arguments.of(SHUTTING_DOWN, SHUTTING_DOWN),
                         Arguments.of(SHUTTING_DOWN, TERMINATED),
                         Arguments.of(TERMINATED, TERMINATED));
    }

    private static Stream<Arguments> validateInstanceStateTransition_failure() {
        return Stream.of(Arguments.of(TERMINATED, STARTING), Arguments.of(TERMINATED, RUNNING),
                         Arguments.of(TERMINATED, SHUTTING_DOWN),
                         Arguments.of(SHUTTING_DOWN, STARTING),
                         Arguments.of(SHUTTING_DOWN, RUNNING));
    }
}
