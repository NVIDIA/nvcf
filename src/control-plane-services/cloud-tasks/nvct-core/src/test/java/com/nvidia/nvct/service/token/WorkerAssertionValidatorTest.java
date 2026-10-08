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
package com.nvidia.nvct.service.token;

import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_1;
import static com.nvidia.nvct.util.TestConstants.TEST_TASK_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.when;

import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.PlainJWT;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.util.MockIcmsServer;
import com.nvidia.nvct.util.MockNotaryServer;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.UUID;
import tools.jackson.databind.json.JsonMapper;
import java.time.Clock;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.Date;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class WorkerAssertionValidatorTest {

    @Autowired
    private WorkerAssertionValidator workerAssertionValidator;

    @Autowired
    private TokenService tokenService;

    @Autowired
    private Clock clock;

    @Value("${nvct.notary.base-url}")
    private String notaryBaseUrl;

    @Value("${spring.security.oauth2.client.registration.notary.client-id}")
    private String notaryClientId;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @Value("${nvct.icms.base-url}")
    private URL icmsBaseUrl;

    @Autowired
    private JsonMapper jsonMapper;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
        MockNotaryServer.start(notaryBaseUrl, notaryClientId);
        MockIcmsServer.start(icmsBaseUrl, jsonMapper);
    }

    @AfterAll
    void cleanup() {
        MockNotaryServer.stop();
        MockNvcfServer.stop();
        MockIcmsServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @BeforeEach
    void setClock() {
        when(clock.instant()).thenReturn(Instant.now().truncatedTo(ChronoUnit.SECONDS));
    }

    @Test
    void shouldAcceptValidSignedWorkerAssertion() {
        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, TEST_TASK_ID_1);

        assertThat(workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1)).isFalse();
    }

    @Test
    void shouldRejectUnsignedWorkerAssertion() {
        var token = new PlainJWT(new JWTClaimsSet.Builder()
                .issuer(notaryBaseUrl)
                .subject(notaryClientId)
                .issueTime(Date.from(Instant.now(clock)))
                .claim("assertion", Map.of("ncaId", TEST_NCA_ID, "taskId", TEST_TASK_ID_1.toString()))
                .build()).serialize();

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class);
    }

    @Test
    void shouldRejectSignedWorkerAssertionWithWrongIssuer() {
        var token = MockNotaryServer.generateSignedWorkerAssertion(
                "http://wrong-notary.example:8080",
                notaryClientId,
                TEST_NCA_ID,
                TEST_TASK_ID_1,
                Instant.now(clock));

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("issuer");
    }

    @Test
    void shouldRejectExpiredWorkerAssertion() {
        var token = MockNotaryServer.generateSignedWorkerAssertion(
                notaryBaseUrl,
                notaryClientId,
                TEST_NCA_ID,
                TEST_TASK_ID_1,
                Instant.now(clock).minus(4, ChronoUnit.HOURS));

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("Expired");
    }

    @Test
    void shouldRejectLegacyTaskMismatchWithoutFallingBackToIntrospection() {
        var token = tokenService.issueWorkerAccessAssertion(TEST_NCA_ID, TEST_TASK_ID_1);
        MockIcmsServer.stubWorkerTokenIntrospectActive(TEST_NCA_ID, TEST_TASK_ID_2, "inst-legacy");
        var callsBefore = MockIcmsServer.workerTokenIntrospectCallCount();

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_2))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("Does not match");
        assertThat(MockIcmsServer.workerTokenIntrospectCallCount()).isEqualTo(callsBefore);
    }

    @Test
    void shouldAcceptDelegatedTokenBoundToRequestedTask() {
        MockIcmsServer.stubWorkerTokenIntrospectActive(TEST_NCA_ID, TEST_TASK_ID_1, "inst-ok");
        var token = fakePsat("inst-ok");

        assertThat(workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1)).isTrue();
    }

    @Test
    void shouldRejectDelegatedTokenBoundToOtherTask() {
        MockIcmsServer.stubWorkerTokenIntrospectActive(TEST_NCA_ID, TEST_TASK_ID_1, "inst-task");
        var token = fakePsat("inst-task");

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_2))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not bound to requested task");
    }

    @Test
    void shouldRejectDelegatedTokenBoundToOtherNca() {
        MockIcmsServer.stubWorkerTokenIntrospectActive("other-nca", TEST_TASK_ID_1, "inst-nca");
        var token = fakePsat("inst-nca");

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not bound to requested task");
    }

    @Test
    void shouldRejectInactiveDelegatedToken() {
        MockIcmsServer.stubWorkerTokenIntrospect("{\"active\": false, \"error\": \"JWT verification failed\"}");
        var token = fakePsat("inst-inactive");

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not active");
    }

    @Test
    void shouldRejectDelegatedTokenWithoutBindingInResponse() {
        MockIcmsServer.stubWorkerTokenIntrospect(
                "{\"active\": true, \"instance_id\": \"inst-unbound\", \"token_type\": \"psat\"}");
        var token = fakePsat("inst-unbound");

        assertThatThrownBy(() -> workerAssertionValidator.validate(token, TEST_NCA_ID, TEST_TASK_ID_1))
                .isInstanceOf(ForbiddenException.class)
                .hasMessageContaining("not active");
    }

    /** Builds an unsigned compact JWS shaped like a projected ServiceAccount token; signature
     *  verification is ICMS's job and is mocked. A unique jti keeps introspection results from
     *  being served across tests from the digest-keyed cache. */
    private static String fakePsat(String jti) {
        var enc = Base64.getUrlEncoder().withoutPadding();
        var payload = ("{\"aud\":[\"nvcf-icms:cl-test\"],\"sub\":\"system:serviceaccount:%s:nvcf-worker\","
                       + "\"exp\":%d,\"jti\":\"%s\"}")
                .formatted(jti, Instant.now().plusSeconds(600).getEpochSecond(), UUID.randomUUID());
        return enc.encodeToString("{\"alg\":\"RS256\",\"kid\":\"k1\"}".getBytes(StandardCharsets.UTF_8))
                + "." + enc.encodeToString(payload.getBytes(StandardCharsets.UTF_8)) + ".c2ln";
    }
}
