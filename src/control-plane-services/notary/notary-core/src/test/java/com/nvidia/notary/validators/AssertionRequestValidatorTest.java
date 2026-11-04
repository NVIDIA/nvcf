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
import static com.nvidia.notary.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.when;

import com.nvidia.notary.config.NotaryProperties;
import com.nvidia.notary.services.JwtResolver;
import com.nvidia.notary.vo.AssertionRequestVo;
import com.nvidia.notary.web.dto.AssertionRequest;
import com.nvidia.boot.exceptions.BadRequestException;
import jakarta.servlet.http.HttpServletRequest;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class AssertionRequestValidatorTest {

    private static final List<String> VALID_SERVICE_ID_LIST = List.of(SERVICE_ID_1);
    private static final Map<String, Object> VALID_DATA = Map.of("key", "value");

    @Mock
    private NotaryProperties notaryPropertiesMock;
    @Mock
    private AudiencesValidator audiencesValidatorMock;
    @Mock
    private JwtResolver jwtResolverMock;
    @Mock
    private HttpServletRequest httpServletRequestMock;
    @Mock
    private Jwt jwtMock;
    @InjectMocks
    private AssertionRequestValidator validator;

    @Test
    void validate_throwsIfRequestBodyIsNull() {
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(null, httpServletRequestMock),
                "Content length must be greater than zero");
    }

    @Test
    void validate_throwsIfServletRequestIsNull() {
        AssertionRequest requestBody = new AssertionRequest();
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(requestBody, null),
                "Content length must be greater than zero");
    }

    @Test
    void validate_throwsIfServletRequestContent() {
        AssertionRequest requestBody = new AssertionRequest();
        when(httpServletRequestMock.getContentLength()).thenReturn(0);
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(requestBody, httpServletRequestMock),
                "Content length must be greater than zero");
    }

    @Test
    void validate_throwsIfServletRequestContentTooLong() {
        AssertionRequest requestBody = new AssertionRequest();
        when(httpServletRequestMock.getContentLength()).thenReturn(10000);
        when(notaryPropertiesMock.getMaxAssertionsRequestSize()).thenReturn(8192L);
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(requestBody, httpServletRequestMock),
                "Content length is greater than the maximum allowed: 8192");
    }

    @ParameterizedTest
    @NullAndEmptySource
    void validate_throwsIfDataIsEmpty(Map<String, Object> data) {
        AssertionRequest requestBody = new AssertionRequest(VALID_SERVICE_ID_LIST, data);
        when(httpServletRequestMock.getContentLength()).thenReturn(1000);
        when(notaryPropertiesMock.getMaxAssertionsRequestSize()).thenReturn(8192L);
        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> validator.validate(requestBody, httpServletRequestMock),
                "Request data is empty");
    }

    @Test
    void validate_pass() {
        when(httpServletRequestMock.getContentLength()).thenReturn(1000);
        when(notaryPropertiesMock.getMaxAssertionsRequestSize()).thenReturn(8192L);
        when(audiencesValidatorMock.getValidatedAudiences(VALID_SERVICE_ID_LIST))
                .thenReturn(VALID_SERVICE_ID_LIST);
        when(jwtResolverMock.getCallerToken()).thenReturn(jwtMock);

        AssertionRequest requestBody = new AssertionRequest(VALID_SERVICE_ID_LIST, VALID_DATA);
        assertThat(validator.validate(requestBody, httpServletRequestMock))
                .isEqualTo(new AssertionRequestVo(jwtMock, VALID_SERVICE_ID_LIST, VALID_DATA));
    }
}