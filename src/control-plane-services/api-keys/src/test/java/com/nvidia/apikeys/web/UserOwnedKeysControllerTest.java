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

import static com.nvidia.apikeys.TestData.KEY_AUTHZ_2;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_REQUEST_1;
import static com.nvidia.apikeys.TestData.SERVICE_ADMIN_USER_ID;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.driver.core.Row;
import com.datastax.driver.core.Session;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keys.KeyDto;
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
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
public class UserOwnedKeysControllerTest extends BaseIntegrationTest {


    @Test
    @SneakyThrows
    void keysPositiveSanityTest() {
        HttpHeaders serviceUserHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        // list before keys created
        ResponseEntity<String> result = restTemplate.exchange(
                "/v1/keys", HttpMethod.GET, new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches("\\{\"keys\":\\[]}");

        // create key
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST, new HttpEntity<>(KEY_REQUEST_1, serviceUserHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[a-zA-Z0-9-_]{64}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\","
                + "\"authorizations\":\\{\"allow\":true}}");

        KeyDto keyDto = jsonMapper.readValue(result.getBody(), KeyDto.class);

        // confirm with list keys
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.GET, new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"keys\":\\[\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\"}]}");

        // retrieve key by id
        result = restTemplate.exchange("/v1/keys/" + keyDto.getId(), HttpMethod.GET,
                                       new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\","
                + "\"authorizations\":\\{\"allow\":true}}");

        // now should be able to introspect key
        HttpHeaders headerForIntrospection = addHeaderForServiceAndUser(null, null, null);
        String audienceServiceId = keyDto.getAudienceServiceIds().iterator().next();
        String introspectionRequest = "{"
                                      + "  \"key\":\"" + keyDto.getValue() + "\","
                                      + "  \"audience_service_id\":\"" + audienceServiceId + "\""
                                      + "}";
        result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST,
                new HttpEntity<>(introspectionRequest, headerForIntrospection), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals(
                "{\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"key_id\":\"" + keyDto.getId() + "\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"authorizations\":{\"allow\":true}}", result.getBody());

        serviceUserHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        // update authorizations should fail now
        String updateAuthz = "{\"authorizations\":" + KEY_AUTHZ_2 + "}";
        result = restTemplate.exchange(
                "/v1/keys/" + keyDto.getId() + "/authorizations",
                HttpMethod.PUT, new HttpEntity<>(updateAuthz, serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.TOO_MANY_REQUESTS);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:too-many-requests\","
                + "\"title\":\"Too Many Requests\",\"status\":429,"
                + "\"detail\":\"Slow down.\","
                + "\"instance\":\"/v1/keys/" + keyDto.getId() + "/authorizations\"}", result.getBody());

        // advancing clock to remove write lock is not going to help
        // removing manually
        session.execute("truncate table row_update_lock;");

        // try update again
        result = restTemplate.exchange(
                "/v1/keys/" + keyDto.getId() + "/authorizations",
                HttpMethod.PUT, new HttpEntity<>(updateAuthz, serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        String patternAfterUpdate =
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\","
                + "\"authorizations\":\\{\"allow\":true,\"other_id\":\"other-id-value-1\"}}";
        assertThat(result.getBody()).matches(patternAfterUpdate);

        result = restTemplate.exchange("/v1/keys/" + keyDto.getId(), HttpMethod.GET,
                                       new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(patternAfterUpdate);

        invalidateCaches();

        // now should be able to introspect key and get new authorizations
        result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST,
                new HttpEntity<>(introspectionRequest, headerForIntrospection), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals(
                "{\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"key_id\":\"" + keyDto.getId() + "\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"authorizations\":{\"allow\":true,\"other_id\":\"other-id-value-1\"}}", result.getBody());

        HttpHeaders serviceAdminHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null
        );

        // query key info by value aka admin lookup
        HttpHeaders superAdminHeader = serviceAdminHeader;
        String adminLookup = "{\"key\":\"" + keyDto.getValue() + "\"}";
        result = restTemplate.exchange(
                "/v1/admin/lookup", HttpMethod.POST,
                new HttpEntity<>(adminLookup, superAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\"}");

        // list user keys in all services as super admin
        result = restTemplate.exchange(
                "/v1/admin/users/" + keyDto.getOwnerId() + "/keys", HttpMethod.GET,
                new HttpEntity<>(superAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"keys\":\\[\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\"}]}");

        // delete key by id
        result = restTemplate.exchange("/v1/keys/" + keyDto.getId(), HttpMethod.DELETE,
                                       new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        assertThat(result.getBody()).isNull();

        var cqlResult = session.execute("select * from keys;");
        Row one = cqlResult.one();
        assertThat(one).isNull();

        // verify owner info is still in reverse lookup table
        cqlResult = session.execute("select * from keys_by_owner_and_service;");
        one = cqlResult.one();
        assertThat(one.getString("owner_type")).isEqualTo("USER");
        assertThat(one.getString("owner_id"))
                .isEqualTo("yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs");
        assertThat(one.getString("owner_status")).isEqualTo("ACTIVE");
        assertThat(one.getTimestamp("owner_status_updated_at")).isNotNull();
        assertThat(one.getString("issuer_service_id")).isNull();
        assertThat(one.getString("key_id")).isNull();
        assertThat(one.getTimestamp("deletes_at")).isNull();
        assertThat(one.getTimestamp("expires_at")).isNull();
        assertThat(one.getString("key_status")).isNull();
        assertThat(one.getString("key_details")).isNull();

        // verify locks not removed
        // we keep them so they expire by TTL which is usually very short
        // mixed deleting the key with delete statement AND by TTL may result in ghost reads
        cqlResult = session.execute("select * from row_update_lock;");
        assertThat(cqlResult.one()).isNotNull();

        // list after delete
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.GET, new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches("\\{\"keys\":\\[]}");

        invalidateCaches();

        // confirm unable to introspect
        result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST,
                new HttpEntity<>(introspectionRequest, headerForIntrospection), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:not-found\","
                + "\"title\":\"Not Found\",\"status\":404,"
                + "\"detail\":\"Key not found\",\"instance\":\"/v1/introspect\"}", result.getBody());

        // confirm unable to lookup
        result = restTemplate.exchange(
                "/v1/admin/lookup", HttpMethod.POST,
                new HttpEntity<>(adminLookup, superAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:not-found\","
                + "\"title\":\"Not Found\",\"status\":404,"
                + "\"detail\":\"Key not found\",\"instance\":\"/v1/admin/lookup\"}", result.getBody());

        TestClock.resetToDefaults();
    }

    @Test
    @SneakyThrows
    void keysShouldExpire() {
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);

        HttpHeaders serviceUserHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        // create key
        var result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST, new HttpEntity<>(KEY_REQUEST_1, serviceUserHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[a-zA-Z0-9-_]{64}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\","
                + "\"authorizations\":\\{\"allow\":true}}");

        KeyDto keyDto = jsonMapper.readValue(result.getBody(), KeyDto.class);

        // Configure clock to simulate lots of time has passed and key expired
        TestClock.setBaseClock(
                TestClock.fixed(KEY_EXPIRES_AT_1.plusSeconds(1), ZoneId.systemDefault()));

        // confirm with list keys
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.GET, new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"keys\":\\[\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}\","
                + "\"status\":\"EXPIRED\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\"}]}");

        // retrieve key by id
        result = restTemplate.exchange("/v1/keys/" + keyDto.getId(), HttpMethod.GET,
                                       new HttpEntity<>(serviceUserHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}\","
                + "\"status\":\"EXPIRED\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"2023-10-03T08:25:24Z\","
                + "\"expires_at\":\"2023-10-04T08:25:24Z\","
                + "\"authorizations\":\\{\"allow\":true}}");

        // introspection will be rejected because key expired
        HttpHeaders headerForIntrospection = addHeaderForServiceAndUser(null, null, null);
        String audienceServiceId = keyDto.getAudienceServiceIds().iterator().next();
        String introspectionRequest =
                "{"
                + "  \"key\":\"" + keyDto.getValue() + "\","
                + "  \"audience_service_id\":\"" + audienceServiceId + "\""
                + "}";
        result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST,
                new HttpEntity<>(introspectionRequest, headerForIntrospection), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:not-found\","
                + "\"title\":\"Not Found\",\"status\":404,\"detail\":\"Key not found\","
                + "\"instance\":\"/v1/introspect\"}", result.getBody());

        TestClock.resetToDefaults();
    }
}
