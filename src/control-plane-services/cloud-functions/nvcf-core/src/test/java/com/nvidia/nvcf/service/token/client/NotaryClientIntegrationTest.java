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
package com.nvidia.nvcf.service.token.client;

import static com.nvidia.nvcf.service.token.client.NotaryClient.CLIENT_REGISTRATION_ID;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.mock.oauth2.MockOAuth2TokenServerInstanced;
import com.nvidia.boot.mock.oauth2.OAuth2TokenServerConfigurationProperties;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.configuration.staticclientauth.StaticClientAuthConfiguration.StaticClientNotaryProperties;
import com.nvidia.nvcf.service.ess.EssService;
import com.nvidia.nvcf.util.MockNotaryServer;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils;
import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.reactive.function.client.WebClient;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@ExtendWith(MockitoExtension.class)
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class NotaryClientIntegrationTest {

    private static final String NOTARY_BASE_URL = "http://localhost:8082";
    private static final String OAUTH_BASE_URL = "http://localhost:8083";
    private static final String CLIENT_ID = "test-client-id";
    private static final String SECRET_ID = "test-secret-id";
    private static final String STATIC_TOKEN = "static-test-token";
    private static final String NVCF_AUDIENCE = "nvcf-audience";
    private static final String ESS_AUDIENCE = "ess-audience";
    private static final String SBS_AUDIENCE = "sbs-audience";
    private static final String TURN_AUDIENCE = "turn-audience";

    @Autowired
    private EssService essService;

    @Autowired
    private ManagedHttpResources notaryHttpResources;

    private MockOAuth2TokenServerInstanced jwtMockServer;

    @BeforeEach
    void setUp() {
        // Start mock notary server
        MockNotaryServer.start(NOTARY_BASE_URL, CLIENT_ID, NVCF_AUDIENCE);

        // Create real OAuth2TokenServerConfigurationProperties
        OAuth2TokenServerConfigurationProperties jwtProperties = new OAuth2TokenServerConfigurationProperties(
                OAUTH_BASE_URL,                                // issuer
                OAUTH_BASE_URL + "/.well-known/jwks.json",     // keysetUrl
                "ES256",                                       // jwsAlgorithm
                null,                                          // serviceBindings
                List.of(CLIENT_ID),                            // clientBindings
                null                                           // customBindings
        );

        // Start OAuth mock server
        jwtMockServer = new MockOAuth2TokenServerInstanced(jwtProperties);
        jwtMockServer.start();
    }

    @AfterEach
    void tearDown() {
        MockNotaryServer.stop();
        if (jwtMockServer != null) {
            jwtMockServer.stop();
        }

    }

    private static NotaryAudiencesConfiguration createAudiencesConfig() {
        NotaryAudiencesConfiguration audiencesConfig = new NotaryAudiencesConfiguration();
        audiencesConfig.setAudiences(Map.of(
                NotaryClient.Audience.NVCF, NVCF_AUDIENCE,
                NotaryClient.Audience.ESS, ESS_AUDIENCE,
                NotaryClient.Audience.SBS, SBS_AUDIENCE,
                NotaryClient.Audience.TURN, TURN_AUDIENCE
        ));
        return audiencesConfig;
    }

    private NotaryClient createClientWithStaticToken() {
        StaticClientNotaryProperties staticProperties = new StaticClientNotaryProperties();
        staticProperties.setToken(STATIC_TOKEN);
        return new NotaryClient(
                essService,
                NOTARY_BASE_URL,
                CLIENT_ID,
                SECRET_ID,
                "make-assertion",
                OAUTH_BASE_URL + "/token",
                true,
                Optional.of(staticProperties),
                createAudiencesConfig(),
                WebClient.builder(),
                notaryHttpResources
        );
    }

    private NotaryClient createClientWithOAuth() {
        return new NotaryClient(
                essService,
                NOTARY_BASE_URL,
                CLIENT_ID,
                SECRET_ID,
                "make-assertion",
                OAUTH_BASE_URL + "/token",
                true,
                Optional.empty(),
                createAudiencesConfig(),
                WebClient.builder(),
                notaryHttpResources
        );
    }

    @Test
    void testIssueInstanceCredentialAssertionToken_WithStaticToken() {
        // Given
        NotaryClient client = createClientWithStaticToken();
        UUID functionId = UUID.randomUUID();
        UUID functionVersionId = UUID.randomUUID();
        String instanceId = "instance-123";
        List<String> instanceIps = List.of("192.168.1.1", "192.168.1.2");

        // When
        String token = client.issueInstanceCredentialAssertionToken(
                functionId, functionVersionId, instanceId, instanceIps);

        // Then
        assertThat(token).isNotNull().isNotBlank();
    }

    @Test
    void testIssueInstanceCredentialAssertionToken_WithOAuth() {
        // Given
        NotaryClient client = createClientWithOAuth();
        UUID functionId = UUID.randomUUID();
        UUID functionVersionId = UUID.randomUUID();
        String instanceId = "instance-123";
        List<String> instanceIps = List.of("192.168.1.1", "192.168.1.2");

        // When
        String token = client.issueInstanceCredentialAssertionToken(
                functionId, functionVersionId, instanceId, instanceIps);

        // Then
        assertThat(token).isNotNull().isNotBlank();
    }
}
