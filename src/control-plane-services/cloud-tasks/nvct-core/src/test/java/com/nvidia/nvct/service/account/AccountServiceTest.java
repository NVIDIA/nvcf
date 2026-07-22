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
package com.nvidia.nvct.service.account;

import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6;
import static com.nvidia.nvct.util.TestConstants.TEST_NCA_ID_WITH_TELEMETRIES_4;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.util.MockNvcfServer;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import java.net.URL;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
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
class AccountServiceTest {

    private static final String HTTP_CLIENT_REQUESTS_METRIC = "http.client.requests";
    private static final String NVCF_ACCOUNT_URI_PREFIX = "/v2/nvcf/accounts";

    @Autowired
    private AccountService accountService;

    @Autowired
    private TestTaskService testTaskService;

    @Autowired
    private MeterRegistry meterRegistry;

    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
        MockNvcfServer.stop();
    }

    @AfterEach
    void afterEach() {
        accountService.invalidateCache();
        testTaskService.clearAll();
    }

    @Test
    void shouldGetAccount() {
        var accountDto = accountService.getAccount(TEST_NCA_ID);
        assertThat(accountDto).isNotNull();
        assertThat(accountDto.ncaId()).isEqualTo(TEST_NCA_ID);
        assertThat(accountDto.clientIds()).hasSize(1);
        assertThat(accountDto.name()).isNotBlank();
        assertThat(accountDto.telemetries()).isNull();
        assertThat(accountDto.registryCredentials()).hasSize(13);
    }

    @Test
    void shouldGetAccountWithTelemetries() {
        var accountDto = accountService.getAccount(TEST_NCA_ID_WITH_TELEMETRIES_4);
        assertThat(accountDto).isNotNull();
        assertThat(accountDto.ncaId()).isEqualTo(TEST_NCA_ID_WITH_TELEMETRIES_4);
        assertThat(accountDto.clientIds()).hasSize(1);
        assertThat(accountDto.name()).isNotBlank();
        assertThat(accountDto.telemetries()).isNotEmpty();
        var telemetryDtos = accountDto.telemetries();
        telemetryDtos.forEach(telemetryDto -> {
            assertThat(telemetryDto.telemetryId()).isNotNull();
            assertThat(telemetryDto.name()).isNotBlank();
            assertThat(telemetryDto.protocol()).isNotNull();
            assertThat(telemetryDto.provider()).isNotNull();
            assertThat(telemetryDto.endpoint()).isNotBlank();
            assertThat(telemetryDto.types()).isNotEmpty();
            assertThat(telemetryDto.createdAt()).isNotNull();
        });
    }

    @Test
    void shouldGetAccountWithoutRegistryCredentials() {
        var accountDto = accountService.getAccount(TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6);
        assertThat(accountDto).isNotNull();
        assertThat(accountDto.ncaId()).isEqualTo(TEST_NCA_ID_WITHOUT_REGISTRY_CREDENTIALS_6);
        assertThat(accountDto.clientIds()).hasSize(1);
        assertThat(accountDto.name()).isNotBlank();
        assertThat(accountDto.registryCredentials()).isNull();
    }

    @Test
    void shouldFailToGetAccount() {
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> accountService.getAccount("non-existent-account"));
    }

    @Test
    void shouldFailToLookupAccount() {
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> accountService.lookupAccountUsingNcaIdOrThrow("non-existent-account"));
    }

    @Test
    void shouldReturnCachedAccountOnSecondCallWithinTtl() {
        MockNvcfServer.getMockNvcfServer().resetRequests();

        accountService.getAccount(TEST_NCA_ID);
        accountService.getAccount(TEST_NCA_ID);

        MockNvcfServer.getMockNvcfServer().verify(1,
                getRequestedFor(urlMatching("/v2/nvcf/accounts/" + TEST_NCA_ID)));
    }

    @Test
    void shouldRefetchAccountAfterTtlExpires() throws InterruptedException {
        MockNvcfServer.getMockNvcfServer().resetRequests();

        accountService.getAccount(TEST_NCA_ID);
        Thread.sleep(3000);
        accountService.getAccount(TEST_NCA_ID);

        MockNvcfServer.getMockNvcfServer().verify(2,
                getRequestedFor(urlMatching("/v2/nvcf/accounts/" + TEST_NCA_ID)));
    }

    @Test
    void shouldRecordMetricsForNvcfResourceServerCall() {
        accountService.invalidateCache();
        MockNvcfServer.getMockNvcfServer().resetRequests();

        var resourceServerRequestCountBefore = nvcfAccountRequestCount();

        accountService.getAccount(TEST_NCA_ID);

        assertThat(nvcfAccountRequestCount()).isGreaterThan(resourceServerRequestCountBefore);
        MockNvcfServer.getMockNvcfServer().verify(1,
                getRequestedFor(urlMatching("/v2/nvcf/accounts/" + TEST_NCA_ID)));
    }

    private long nvcfAccountRequestCount() {
        return meterRegistry.find(HTTP_CLIENT_REQUESTS_METRIC)
                .timers()
                .stream()
                .filter(this::isNvcfAccountRequestTimer)
                .mapToLong(Timer::count)
                .sum();
    }

    private boolean isNvcfAccountRequestTimer(Timer timer) {
        return isNvcfAccountTagValue(timer, "http.route")
                || isNvcfAccountTagValue(timer, "uri");
    }

    private boolean isNvcfAccountTagValue(Timer timer, String tagKey) {
        var tagValue = timer.getId().getTag(tagKey);
        return tagValue != null && tagValue.contains(NVCF_ACCOUNT_URI_PREFIX);
    }

}
