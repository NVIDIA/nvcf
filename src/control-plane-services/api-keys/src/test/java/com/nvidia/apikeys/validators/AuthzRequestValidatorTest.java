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

import static com.nvidia.apikeys.TestData.API_KEY_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.lenient;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.dto.authz.ApiKeyInput;
import com.nvidia.apikeys.dto.authz.AuthzRequest;
import com.nvidia.apikeys.dto.introspection.IntrospectionRequest;
import com.nvidia.apikeys.vo.PolicyEvaluationRequestVo;
import java.util.Map;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EmptySource;
import org.junit.jupiter.params.provider.NullSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class AuthzRequestValidatorTest {

    private static final String NAMESPACE = "nvcf";

    @Mock
    private NakProperties nakProperties;

    @InjectMocks
    private AuthzRequestValidator validator;

    @BeforeEach
    void setUp() {
        lenient().when(nakProperties.getServiceIdMap())
                .thenReturn(Map.of(NAMESPACE, SERVICE_ID_1));
    }

    @ParameterizedTest
    @ValueSource(strings = {"apikey.allow", "anything.allow"})
    void validate_shouldAcceptWildcardAllowRuleNames(String ruleName) {
        AuthzRequest request = authzRequestWithKey(API_KEY_1);

        PolicyEvaluationRequestVo result = validator.validate(NAMESPACE, ruleName, request);

        assertThat(result).isEqualTo(PolicyEvaluationRequestVo.builder()
                .namespace(NAMESPACE)
                .policyName(ruleName)
                .introspectionRequest(IntrospectionRequest.builder()
                        .key(API_KEY_1)
                        .audienceServiceId(SERVICE_ID_1)
                        .build())
                .build());
    }

    @ParameterizedTest
    @NullSource
    @EmptySource
    @ValueSource(strings = {"apikey.deny", "allow", ".allow", "apikey.allow.extra", "apikey allow"})
    void validate_shouldRejectUnsupportedRuleName(String ruleName) {
        AuthzRequest request = authzRequestWithKey(API_KEY_1);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(NAMESPACE, ruleName, request),
                "Rule name is not supported: " + ruleName);
    }

    @Test
    void validate_shouldThrowWhenRequestIsNull() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(NAMESPACE, "apikey.allow", null),
                "Api key is not provided");
    }

    @Test
    void validate_shouldThrowWhenApiKeyInputMissing() {
        AuthzRequest request = new AuthzRequest(null);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(NAMESPACE, "apikey.allow", request),
                "Api key is not provided");
    }

    @ParameterizedTest
    @NullSource
    @EmptySource
    void validate_shouldThrowWhenApiKeyMissing(String apiKey) {
        AuthzRequest request = authzRequestWithKey(apiKey);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(NAMESPACE, "apikey.allow", request),
                "Api key is not provided");
    }

    @Test
    void validate_shouldThrowWhenNamespaceNotConfigured() {
        AuthzRequest request = authzRequestWithKey(API_KEY_1);

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate("unknown-ns", "apikey.allow", request),
                "Namespace 'unknown-ns' is not configured");
    }

    private static AuthzRequest authzRequestWithKey(String apiKey) {
        return new AuthzRequest(ApiKeyInput.builder().apiKey(apiKey).build());
    }
}
