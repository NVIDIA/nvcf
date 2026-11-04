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
package com.nvidia.notary.validators;

import static com.nvidia.notary.utils.TestData.SERVICE_ID_1;
import static com.nvidia.notary.utils.TestData.SERVICE_ID_2;
import static com.nvidia.notary.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.exceptions.BadRequestException;
import java.util.List;
import java.util.stream.Stream;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.mockito.InjectMocks;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class AudiencesValidatorTest {

    @InjectMocks
    private AudiencesValidator validator;

    private static Stream<Arguments> validServicesLists() {
        return Stream.of(
                Arguments.of(List.of(SERVICE_ID_2)),
                Arguments.of(List.of(SERVICE_ID_2, SERVICE_ID_1))
        );
    }

    @MethodSource("validServicesLists")
    @ParameterizedTest
    void getValidatedAudiences_passForValidAudiences(List<String> audiences) {
        assertThat(validator.getValidatedAudiences(audiences)).isEqualTo(audiences);
    }

    @ParameterizedTest
    @NullAndEmptySource
    void getValidatedAudiences_throwsForEmptyAudiences(List<String> audiences) {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.getValidatedAudiences(audiences),
                "Audience services must have at least one audience");
    }

}
