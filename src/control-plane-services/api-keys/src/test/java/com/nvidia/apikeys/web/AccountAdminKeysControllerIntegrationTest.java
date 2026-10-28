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

package com.nvidia.apikeys.web;

import static com.nvidia.apikeys.TestData.KEY_AUTHZ_1;
import static com.nvidia.apikeys.TestData.KEY_DESCRIPTION_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1_STRING;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusesResponse;
import com.nvidia.apikeys.dto.keysV2.KeyResponse;
import com.nvidia.apikeys.dto.keysV2.ListKeysResponse;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import java.time.ZoneId;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

/**
 * Routing for the account-admin prefix. {@link com.nvidia.apikeys.facade.KeyManagementFacadeIntegrationTest}
 * covers the shared behavior; these tests only check that the controller wires the right actor
 * and path to the facade.
 */
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class AccountAdminKeysControllerIntegrationTest extends BaseIntegrationTest {

    private static final String NCA_ID = "nca-1";
    private static final String CREATE_PATH =
            "/v2/api-keys/account-admin/accounts/" + NCA_ID + "/services/" + SERVICE_ID_1
                    + "/users/" + USER_KEY_OWNER_ID_1 + "/keys";

    @BeforeEach
    void setUp() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));
    }

    @AfterEach
    void tearDown() {
        TestClock.resetToDefaults();
    }

    @Test
    void listsKeysByAccountAcrossIssuerServices() {
        var createBody = """
                {"description":"%s","expiresAt":"%s","authorizations":%s}
                """.formatted(KEY_DESCRIPTION_1, KEY_EXPIRES_AT_1_STRING, KEY_AUTHZ_1);
        exchange(HttpMethod.POST, CREATE_PATH, createBody, KeyResponse.class);

        var listed = exchange(
                HttpMethod.GET, "/v2/api-keys/account-admin/accounts/" + NCA_ID + "/keys", null,
                ListKeysResponse.class);
        assertThat(listed.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(listed.getBody().getKeys()).hasSize(1);
    }

    @Test
    void adminManagesASuspendedUsersKeys() {
        var statusPath =
                "/v2/api-keys/account-admin/accounts/" + NCA_ID + "/users/" + USER_KEY_OWNER_ID_1
                        + "/status";
        var suspend = exchange(
                HttpMethod.PUT, statusPath, "{\"status\":\"SUSPENDED\"}",
                KeyOwnerStatusResponse.class);
        assertThat(suspend.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(suspend.getBody().getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);

        var createBody = """
                {"description":"%s","expiresAt":"%s","authorizations":%s}
                """.formatted(KEY_DESCRIPTION_1, KEY_EXPIRES_AT_1_STRING, KEY_AUTHZ_1);
        var created = exchange(HttpMethod.POST, CREATE_PATH, createBody, KeyResponse.class);
        assertThat(created.getStatusCode()).isEqualTo(HttpStatus.OK);

        var statuses = exchange(
                HttpMethod.GET,
                "/v2/api-keys/account-admin/accounts/" + NCA_ID + "/users/"
                        + USER_KEY_OWNER_ID_1 + "/statuses",
                null, KeyOwnerStatusesResponse.class);
        assertThat(statuses.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(statuses.getBody().getAccounts()).singleElement().satisfies(
                account -> assertThat(account.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED));
    }

    @Test
    void accountAdminHasNoCrossAccountStatusesRoute() {
        var response = restTemplate.exchange(
                "/v2/api-keys/account-admin/users/" + USER_KEY_OWNER_ID_1 + "/statuses",
                HttpMethod.GET, new HttpEntity<String>((HttpHeaders) null), String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    private <T> ResponseEntity<T> exchange(
            HttpMethod method, String path, String body, Class<T> type) {
        var headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        return restTemplate.exchange(path, method, new HttpEntity<>(body, headers), type);
    }
}
