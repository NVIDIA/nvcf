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
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusesResponse;
import com.nvidia.apikeys.dto.keysV2.KeyResponse;
import com.nvidia.apikeys.dto.keysV2.KeyStatusV2;
import com.nvidia.apikeys.dto.keysV2.ListKeysResponse;
import com.nvidia.apikeys.utils.TestClock;
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
 * Routing for the key-owner prefix. {@link com.nvidia.apikeys.facade.KeyManagementFacadeIntegrationTest}
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
class KeyOwnerKeysControllerIntegrationTest extends BaseIntegrationTest {

    private static final String NCA_ID = "nca-1";
    private static final String CREATE_PATH =
            "/v2/api-keys/key-owner/accounts/" + NCA_ID + "/services/" + SERVICE_ID_1
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
    void createGetUpdateAndDeleteRoundTripThroughHttp() {
        var createBody = """
                {"description":"%s","expiresAt":"%s","authorizations":%s}
                """.formatted(KEY_DESCRIPTION_1, KEY_EXPIRES_AT_1_STRING, KEY_AUTHZ_1);
        var created = exchange(HttpMethod.POST, CREATE_PATH, createBody, KeyResponse.class);
        assertThat(created.getStatusCode()).isEqualTo(HttpStatus.OK);
        var key = created.getBody();
        assertThat(key.getValue()).doesNotContain("*");
        assertThat(key.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(key.getAccountId()).isEqualTo(NCA_ID);

        var keyPath = CREATE_PATH + "/" + key.getId();

        var fetched = exchange(HttpMethod.GET, keyPath, null, KeyResponse.class);
        assertThat(fetched.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(fetched.getBody().getValue()).contains("**********");

        var suspended = exchange(
                HttpMethod.PUT, keyPath + "/status", "{\"status\":\"SUSPENDED\"}",
                KeyResponse.class);
        assertThat(suspended.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(suspended.getBody().getStatus()).isEqualTo(KeyStatusV2.SUSPENDED);

        var deleted = restTemplate.exchange(
                keyPath, HttpMethod.DELETE, new HttpEntity<String>((HttpHeaders) null),
                Void.class);
        assertThat(deleted.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        var afterDelete = restTemplate.exchange(
                keyPath, HttpMethod.GET, new HttpEntity<String>((HttpHeaders) null), String.class);
        assertThat(afterDelete.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @Test
    void routeMismatchReturnsNotFound() {
        var createBody = """
                {"description":"%s","expiresAt":"%s","authorizations":%s}
                """.formatted(KEY_DESCRIPTION_1, KEY_EXPIRES_AT_1_STRING, KEY_AUTHZ_1);
        var created = exchange(HttpMethod.POST, CREATE_PATH, createBody, KeyResponse.class);
        var keyId = created.getBody().getId();

        var wrongAccount =
                "/v2/api-keys/key-owner/accounts/nca-2/services/" + SERVICE_ID_1
                        + "/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyId;
        var response = restTemplate.exchange(
                wrongAccount, HttpMethod.GET, new HttpEntity<String>((HttpHeaders) null),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @Test
    void listByUserQueriesAcrossAccountsAndPages() {
        var createBody = """
                {"description":"%s","expiresAt":"%s","authorizations":%s}
                """.formatted(KEY_DESCRIPTION_1, KEY_EXPIRES_AT_1_STRING, KEY_AUTHZ_1);
        exchange(HttpMethod.POST, CREATE_PATH, createBody, KeyResponse.class);

        var listPath = "/v2/api-keys/key-owner/users/" + USER_KEY_OWNER_ID_1 + "/keys"
                + "?nca-id=" + NCA_ID + "&service-id=" + SERVICE_ID_1;
        var listed = exchange(HttpMethod.GET, listPath, null, ListKeysResponse.class);
        assertThat(listed.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(listed.getBody().getKeys()).hasSize(1);
    }

    @Test
    void ownerStatusesListPathReturnsActiveByDefault() {
        var path = "/v2/api-keys/key-owner/users/" + USER_KEY_OWNER_ID_1 + "/statuses";
        var response = exchange(HttpMethod.GET, path, null, KeyOwnerStatusesResponse.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody().getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
    }

    @Test
    void keyStatusUpdateWithoutStatusIsRejectedAndKeepsTheKeyStatus() {
        var createBody = """
                {"description":"%s","expiresAt":"%s","authorizations":%s}
                """.formatted(KEY_DESCRIPTION_1, KEY_EXPIRES_AT_1_STRING, KEY_AUTHZ_1);
        var created = exchange(HttpMethod.POST, CREATE_PATH, createBody, KeyResponse.class);
        var keyPath = CREATE_PATH + "/" + created.getBody().getId();

        var rejected = exchange(HttpMethod.PUT, keyPath + "/status", "{}", String.class);
        assertThat(rejected.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        var fetched = exchange(HttpMethod.GET, keyPath, null, KeyResponse.class);
        assertThat(fetched.getBody().getStatus()).isEqualTo(KeyStatusV2.ACTIVE);
    }

    private <T> ResponseEntity<T> exchange(
            HttpMethod method, String path, String body, Class<T> type) {
        var headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        return restTemplate.exchange(path, method, new HttpEntity<>(body, headers), type);
    }
}
