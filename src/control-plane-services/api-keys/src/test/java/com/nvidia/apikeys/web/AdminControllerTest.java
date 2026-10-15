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

import static com.nvidia.apikeys.TestData.KEY_REQUEST_1;
import static com.nvidia.apikeys.TestData.KEY_REQUEST_2;
import static com.nvidia.apikeys.TestData.SERVICE_ADMIN_USER_ID;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.utils.TestClock;
import java.time.ZoneId;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
public class AdminControllerTest extends BaseIntegrationTest {

    @SneakyThrows
    @Test
    void bulkDelete_shouldDeleteAllUserKeysAcrossServices() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        HttpHeaders userHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);
        HttpHeaders adminHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        // create two keys for the same owner
        var create1 = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(KEY_REQUEST_1, userHeaders), String.class);
        assertThat(create1.getStatusCode()).isEqualTo(HttpStatus.OK);
        var create2 = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(KEY_REQUEST_2, userHeaders), String.class);
        assertThat(create2.getStatusCode()).isEqualTo(HttpStatus.OK);

        // sanity-check: admin list-all returns both
        var listBefore = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(adminHeaders), String.class);
        assertThat(listBefore.getStatusCode()).isEqualTo(HttpStatus.OK);
        ListKeysResponse beforeBody = jsonMapper.readValue(
                listBefore.getBody(), ListKeysResponse.class);
        assertThat(beforeBody.getKeys()).hasSize(2);

        // bulk-delete
        var deleteResult = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.DELETE,
                new HttpEntity<>(adminHeaders), String.class);
        assertThat(deleteResult.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        assertThat(deleteResult.getBody()).isNull();

        // verify cross-service admin list is empty
        var listAfterAdmin = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(adminHeaders), String.class);
        assertThat(listAfterAdmin.getStatusCode()).isEqualTo(HttpStatus.OK);
        ListKeysResponse afterAdminBody = jsonMapper.readValue(
                listAfterAdmin.getBody(), ListKeysResponse.class);
        assertThat(afterAdminBody.getKeys()).isEmpty();

        // verify per-service-admin list is empty
        var listAfterServiceAdmin = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(adminHeaders), String.class);
        assertThat(listAfterServiceAdmin.getStatusCode()).isEqualTo(HttpStatus.OK);
        ListKeysResponse afterSvcBody = jsonMapper.readValue(
                listAfterServiceAdmin.getBody(), ListKeysResponse.class);
        assertThat(afterSvcBody.getKeys()).isEmpty();

        TestClock.resetToDefaults();
    }

    @SneakyThrows
    @Test
    void bulkDelete_shouldReturnNotFoundWhenUserDoesNotExist() {
        HttpHeaders adminHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        var result = restTemplate.exchange(
                "/v1/admin/users/no-such-user/keys", HttpMethod.DELETE,
                new HttpEntity<>(adminHeaders), String.class);
        // the AdminFacade.deleteUserKeys path resolves the owner first via
        // keyOwnerService.loadExistingKeyOwner, which throws NotFound when the owner row
        // is absent. Asserting that the bulk-delete refuses to operate on an unknown
        // owner — guards against silent no-op delete on typos.
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
    }

    @SneakyThrows
    @Test
    void bulkDelete_shouldBeIdempotentWhenUserHasNoKeys() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        HttpHeaders userHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);
        HttpHeaders adminHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        // create then delete a single key so the owner row exists but has no keys
        var create = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(KEY_REQUEST_1, userHeaders), String.class);
        assertThat(create.getStatusCode()).isEqualTo(HttpStatus.OK);
        KeyDto key = jsonMapper.readValue(create.getBody(), KeyDto.class);
        var del = restTemplate.exchange(
                "/v1/keys/" + key.getId(), HttpMethod.DELETE,
                new HttpEntity<>(userHeaders), String.class);
        assertThat(del.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        // bulk-delete should still succeed
        var result = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.DELETE,
                new HttpEntity<>(adminHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);

        TestClock.resetToDefaults();
    }
}
