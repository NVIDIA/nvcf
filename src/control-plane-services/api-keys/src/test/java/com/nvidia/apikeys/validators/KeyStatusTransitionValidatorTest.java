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

package com.nvidia.apikeys.validators;

import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static com.nvidia.apikeys.vo.KeyStatus.ACTIVE;
import static com.nvidia.apikeys.vo.KeyStatus.SUSPENDED;
import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.boot.exceptions.BadRequestException;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.junit.jupiter.params.provider.EnumSource.Mode;
import org.mockito.InjectMocks;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class KeyStatusTransitionValidatorTest {

    @InjectMocks
    KeyStatusTransitionValidator validator;

    @ParameterizedTest
    @EnumSource(KeyStatus.class)
    void assertStatusTransitionValid_throwsIfCurrentStatusNull(KeyStatus desired) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertStatusTransitionValid(null, desired),
                "Invalid status transition");
    }

    @ParameterizedTest
    @EnumSource(KeyStatus.class)
    void assertStatusTransitionValid_throwsIfDesiredStatusNull(KeyStatus currentStatus) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertStatusTransitionValid(currentStatus, null),
                "Invalid status transition");
    }

    @ParameterizedTest
    @EnumSource(value = KeyStatus.class, names = "SUSPENDED", mode = Mode.EXCLUDE)
    void assertStatusTransitionValid_throwsIfDesiredStatusNotAllowed_fromExpired(
            KeyStatus desired) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertStatusTransitionValid(KeyStatus.EXPIRED, desired),
                "Invalid status transition");
    }

    @ParameterizedTest
    @EnumSource(value = KeyStatus.class, names = {"SUSPENDED" }, mode = Mode.EXCLUDE)
    void assertStatusTransitionValid_throwsIfDesiredStatusNotAllowed_fromActive(KeyStatus desired) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertStatusTransitionValid(ACTIVE, desired),
                "Invalid status transition");
    }

    @ParameterizedTest
    @EnumSource(value = KeyStatus.class, names = {"ACTIVE" }, mode = Mode.EXCLUDE)
    void assertStatusTransitionValid_throwsIfDesiredStatusNotAllowed_fromSuspended(
            KeyStatus desired) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.assertStatusTransitionValid(SUSPENDED, desired),
                "Invalid status transition");
    }

    @Test
    void assertStatusTransitionValid_passIfAllowed() {
        assertDoesNotThrow(() -> validator.assertStatusTransitionValid(ACTIVE, SUSPENDED));
        assertDoesNotThrow(() -> validator.assertStatusTransitionValid(SUSPENDED, ACTIVE));
    }
}
