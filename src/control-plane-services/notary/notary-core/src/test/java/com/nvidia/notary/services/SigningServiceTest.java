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
package com.nvidia.notary.services;

import static com.nvidia.notary.utils.TestData.SERVICE_ID_1;
import static com.nvidia.notary.utils.TestData.SERVICE_ID_2;
import static com.nvidia.notary.utils.TestData.TEST_JTI;
import static com.nvidia.notary.utils.TestData.TEST_TIME_DATE;
import static com.nvidia.notary.utils.TestData.TEST_TIME_INSTANT;
import static org.assertj.core.api.Assertions.assertThat;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.notary.config.NotaryProperties;
import com.nvidia.notary.vo.AssertionRequestVo;
import java.text.ParseException;
import java.time.Clock;
import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.security.oauth2.jwt.Jwt;

@ExtendWith(MockitoExtension.class)
class SigningServiceTest {

    @Mock
    private JwtService jwtServiceMock;
    @Spy
    private NotaryProperties notaryPropertiesMock = NotaryProperties.builder()
            .signingKid("signing-kid")
            .issuerUrl("issuer-url")
            .signingAlgorithm(JWSAlgorithm.ES256)
            .build();
    @Mock
    private Clock clockMock;
    @Mock
    private JtiGenerator jtiGeneratorMock;
    @Mock
    private Jwt jwtMock;
    @Captor
    private ArgumentCaptor<SignedJWT> signedJWTArgumentCaptor;

    @InjectMocks
    private SigningService signingService;

    @Test
    void sign()
            throws ParseException {
        List<String> validServiceIds = List.of(SERVICE_ID_1, SERVICE_ID_2);
        Map<String, Object> data = Map.of("key", List.of("v1", "v2"));

        when(clockMock.instant()).thenReturn(TEST_TIME_INSTANT);
        when(jtiGeneratorMock.generate()).thenReturn(TEST_JTI);
        when(jwtMock.getSubject()).thenReturn("caller-client-id");

        var validatedRequest = new AssertionRequestVo(jwtMock, validServiceIds, data);
        SignedJWT signedJWT = signingService.sign(validatedRequest);

        verify(jwtServiceMock).signJwt(signedJWTArgumentCaptor.capture(), eq("signing-kid"));
        assertThat(signedJWT).isEqualTo(signedJWTArgumentCaptor.getValue());

        assertNotNull(signedJWT);

        assertThat(signedJWT.getJWTClaimsSet().getClaims()).containsExactlyInAnyOrderEntriesOf(
                Map.of(
                        "iss", "issuer-url",
                        "jti", TEST_JTI,
                        "sub", "caller-client-id",
                        "aud", validServiceIds,
                        "assertion", data,
                        "iat", TEST_TIME_DATE
                ));
        assertThat(signedJWT.getHeader().toJSONObject()).containsExactlyInAnyOrderEntriesOf(
                Map.of(
                        "kid", "signing-kid",
                        "alg", "ES256"
                ));
    }
}