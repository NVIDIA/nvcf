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
package com.nvidia.notary.integration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpMethod.GET;

import com.github.tomakehurst.wiremock.WireMockServer;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServer;
import com.nvidia.boot.mock.oauth2.OAuth2TestUtils;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import com.nvidia.notary.services.JtiGenerator;
import com.nvidia.notary.utils.TestData;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Date;
import java.util.List;
import java.util.Map;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpStatus;
import org.springframework.web.context.request.RequestContextHolder;

public abstract class BaseIntegrationTest {

    protected static MockOAuth2TokenServer oauth2MockServer;
    protected static WireMockServer oauth2WireMockServer;

    private static final OAuth2TokenServerConfigurationProperties OAUTH2_TOKEN_SERVER_CONFIGURATION =
            new OAuth2TokenServerConfigurationProperties(
                    "http://xqlnhnfz6tjcz-ashudoahdkz6zrpjttwhaxjhaketm.localhost.local:8081",
                    "http://xqlnhnfz6tjcz-ashudoahdkz6zrpjttwhaxjhaketm.localhost.local:8081/.well-known/jwks.json",
                    "ES256",
                    List.of(), List.of(), null);


    @Autowired
    protected TestRestTemplate restTemplate;

    @Value("${spring.security.oauth2.resourceserver.jwt.issuer-uri}")
    protected String oauth2Issuer;

    @MockitoBean
    protected JtiGenerator jtiGeneratorMock;

    @BeforeAll
    static void init() {
        oauth2MockServer = new MockOAuth2TokenServer(OAUTH2_TOKEN_SERVER_CONFIGURATION);
        oauth2WireMockServer = oauth2MockServer.start();
    }

    @AfterAll
    static void afterAll() {
        oauth2MockServer.stop();
    }

    @BeforeEach
    public void initBefore() {
        RequestContextHolder.resetRequestAttributes();
    }


    @SneakyThrows
    protected String getAccessToken(List<String> scopes, String sub, String issuer, Instant issuedAt) {
        return getAccessTokenWithAudiences(
                scopes,
                sub,
                issuer,
                issuedAt,
                List.of("s:" + TestData.SERVICE_ID_1, sub, "s:localhost"));
    }

    @SneakyThrows
    protected String getAccessTokenWithAudiences(List<String> scopes, String sub, String issuer,
                                              Instant issuedAt, List<String> audiences) {
        Instant expiresAt = issuedAt.plus(15, ChronoUnit.MINUTES);
        var claimsSetBuilder = new JWTClaimsSet.Builder()
                .subject(sub)
                .issueTime(Date.from(issuedAt))
                .expirationTime(Date.from(expiresAt))
                .claim("scopes", scopes)
                .claim("token_type", "service_account")
                .claim("service", Map.of(
                        "id", TestData.SERVICE_ID_1,
                        "name", "actor-service-name"))
                .audience(audiences)
                .issuer(issuer)
                .claim("azp", sub);

        return OAuth2TestUtils.getJwt(claimsSetBuilder);
    }

    protected String getNotaryServicePublicKeys() {
        var result = restTemplate.exchange(
                "/.well-known/jwks.json", GET, HttpEntity.EMPTY, String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).isEqualTo("""
           {"keys":[\
           {"kty":"EC","crv":"P-256","kid":"E4a21067-0089-4c64-b3de-dc76e2f95e7f",\
           "x":"VrKv7bxxmB5F8hS6w8mnVspfY_wASD2AF8sB0biHZZo",\
           "y":"v9Kfxtd9ePO_QpPmaH3Qp8aG4zx_Wz7XamLOhb9KZJk",\
           "alg":"ES256"},\
           {"kty":"EC","crv":"P-256","kid":"f4a21067-0082-4c64-b3de-dc76e2f95e7f",\
           "x":"VrKv7bxxmB5F8hS6w8mnVspfY_wASD2AF8sB0biHZZo",\
           "y":"v9Kfxtd9ePO_QpPmaH3Qp8aG4zx_Wz7XamLOhb9KZJk",\
           "alg":"ES256"}]}""");

        return result.getBody();
    }

}
