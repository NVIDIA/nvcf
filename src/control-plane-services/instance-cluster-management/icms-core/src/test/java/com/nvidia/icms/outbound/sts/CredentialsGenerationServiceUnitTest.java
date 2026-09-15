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
package com.nvidia.icms.outbound.sts;

import com.amazonaws.arn.Arn;
import com.nvidia.icms.configuration.aws.AwsConfigurationProperties;
import java.util.stream.Stream;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CredentialsGenerationServiceUnitTest {

    CredentialsGenerationService credentialsGenerationService;

    @Mock
    AwsConfigurationProperties awsConfigurationProperties;

    @BeforeEach
    void beforeEach() {
        credentialsGenerationService = new CredentialsGenerationService(null,
                                                                        awsConfigurationProperties);
    }

    @MethodSource
    @ParameterizedTest
    void arnFromUrl_success(String queueUrl, String accountId, String queueName) {
        // Act
        String arnString = credentialsGenerationService.arnFromUrl(queueUrl);

        // Verify
        Arn arn = Arn.fromString(arnString);
        Assertions.assertEquals(accountId, arn.getAccountId());
        Assertions.assertEquals(queueName, arn.getResource().toString());
    }

    private static Stream<Arguments> arnFromUrl_success() {
        return Stream.of(
                // Local host url
                Arguments.of(
                        "http://localhost:4566/000000000000/gdn-spot-instance-requests-global.fifo",
                        "000000000000",
                        "gdn-spot-instance-requests-global.fifo"),
                // Local host url
                Arguments.of(
                        "https://sqs.us-east-1.amazonaws.com/123456789123/q_gdn_spot_byoc_test.fifo",
                        "123456789123",
                        "q_gdn_spot_byoc_test.fifo")
        );
    }
}
