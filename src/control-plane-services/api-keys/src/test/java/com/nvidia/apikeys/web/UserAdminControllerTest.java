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
import static com.nvidia.apikeys.TestData.KEY_REQUEST_1;
import static com.nvidia.apikeys.TestData.KEY_REQUEST_2;
import static com.nvidia.apikeys.TestData.SERVICE_ADMIN_USER_ID;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static com.nvidia.apikeys.config.IntegrationTestConfiguration.KEY_SPACE;
import static com.nvidia.apikeys.vo.KeyOwnerStatus.ACTIVE;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.tuple;

import com.datastax.driver.core.Session;
import com.nvidia.apikeys.App;
import com.nvidia.apikeys.TestData;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.services.ServicesService;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import java.time.Instant;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import java.time.temporal.ChronoUnit;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
public class UserAdminControllerTest extends BaseIntegrationTest {

    private static final String RESPONSE_MATCHER_KEY_1_CREATED =
            """
                    \\{"id":"[a-z0-9-]{36}",\
                    "value":"nvcfapi-test-[a-zA-Z0-9-_]{64}",\
                    "status":"ACTIVE",\
                    "owner_type":"USER",\
                    "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                    "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                    "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                    "description":"123e4567-description",\
                    "created_at":"2023-10-03T08:25:24Z",\
                    "expires_at":"2023-10-04T08:25:24Z",\
                    "authorizations":\\{"allow":true}}""";

    private static final String ERROR_KEY_NOT_FOUND =
            """
                    {"type":"urn:nv-boot:problem-details:not-found","title":"Not Found",\
                    "status":404,"detail":"Key not found.","instance":"/v1/introspect"}""";

    @Autowired
    ServicesService servicesService;

    @Autowired
    KeysDao keysDao;

    @SneakyThrows
    @Test
    void adminSanityTests() {
        // seed service
        Session session = IntegrationTestConfiguration.CQL_SESSION;
        session.execute("use " + KEY_SPACE);
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        HttpHeaders serviceAdminHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        // Get user key by admin should throw when key does not exist
        var result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/invalid", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:not-found",\
                        "title":"Not Found","status":404,\
                        "detail":"Key not found",\
                        "instance":"/v1/service-admin/users/yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs/keys/invalid"}""", result.getBody());

        // create key
        HttpHeaders serviceUserHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(KEY_REQUEST_1, serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(RESPONSE_MATCHER_KEY_1_CREATED);
        KeyDto keyDto = jsonMapper.readValue(result.getBody(), KeyDto.class);

        // Should be able to get the key details
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId(),
                HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"ACTIVE",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z",\
                        "authorizations":\\{"allow":true}}""");

        // Should be able to list keys
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"keys":\\[\\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"ACTIVE",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z"}]}""");

        // should be able to introspect key while active
        // now should be able to introspect key
        HttpHeaders headerForIntrospection = new HttpHeaders();
        headerForIntrospection.setContentType(MediaType.APPLICATION_JSON);
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

        // Should be able to set key status to SUSPENDED
        String updateStatusSuspended = "{\"status\":\"SUSPENDED\"}";
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId()
                + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusSuspended, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"SUSPENDED",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z",\
                        "authorizations":\\{"allow":true}}""");

        // Should be able to list keys
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"keys":\\[\\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"SUSPENDED",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z"}]}""");

        // verify status is SUSPENDED
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId(),
                HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"SUSPENDED",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z",\
                        "authorizations":\\{"allow":true}}""");

        invalidateCaches();

        // should fail to introspect SUSPENDED key
        result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST,
                new HttpEntity<>(introspectionRequest, headerForIntrospection), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:not-found",\
                        "title":"Not Found","status":404,\
                        "detail":"Key not found","instance":"/v1/introspect"}""", result.getBody());

        // Should be unable to set key status to SUSPENDED when it is already suspended
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId()
                + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusSuspended, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:bad-request\","
                + "\"title\":\"Bad Request\",\"status\":400,"
                + "\"detail\":\"Invalid status transition\","
                + "\"instance\":\"/v1/service-admin/users/yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs/keys/"
                + keyDto.getId() + "/status\"}", result.getBody());

        // RE-ACTIVATE KEY
        String updateStatusActive = "{\"status\":\"ACTIVE\"}";
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId()
                + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusActive, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"ACTIVE",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z",\
                        "authorizations":\\{"allow":true}}""");

        // verify status changed to ACTIVE
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId(),
                HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"ACTIVE",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-04T08:25:24Z",\
                        "authorizations":\\{"allow":true}}""");

        // modify keys expiration date to make it expired
        KeyByOwnerAndServiceVo keyByOwnerAndServiceVo = keysDao.getKeyByOwnerAndServiceAndId(
                USER, USER_KEY_OWNER_ID_1, keyDto.getIssuerServiceId(), keyDto.getId()).get();
        String keyHash = keyByOwnerAndServiceVo.getKeyHash();
        KeyVo keyVo = keysDao.getKeyByHash(keyHash).get();
        keyVo = keyVo.toBuilder()
                .expiresAt(TEST_TIME.minus(1, ChronoUnit.DAYS))
                .build();
        KeyOwnerVo keyOwnerVo = new KeyOwnerVo(USER, USER_KEY_OWNER_ID_1, ACTIVE, TEST_TIME);
        keysDao.save(keyVo, keyOwnerVo);

        // verify status changed to EXPIRED
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId(),
                HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                """
                        \\{"id":"[a-z0-9-]{36}",\
                        "value":"nvcfapi-test-[*]{10}[a-zA-Z0-9-_]{3}",\
                        "status":"EXPIRED",\
                        "owner_type":"USER",\
                        "owner_id":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "issuer_service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "audience_service_ids":\\["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "description":"123e4567-description",\
                        "created_at":"2023-10-03T08:25:24Z",\
                        "expires_at":"2023-10-02T08:25:24Z",\
                        "authorizations":\\{"allow":true}}""");

        // Should be able to suspend key
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId()
                + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusSuspended, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).contains("\"status\":\"SUSPENDED\"");

        // RE-ACTIVATE KEY
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/" + keyDto.getId()
                + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusActive, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).contains("\"status\":\"EXPIRED\"");

        // delete key by id
        result = restTemplate.exchange("/v1/keys/" + keyDto.getId(), HttpMethod.DELETE,
                                       new HttpEntity<>(serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NO_CONTENT);
        assertThat(result.getBody()).isNull();

        // Get user key by admin should throw when key does not exist
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys/invalid", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:not-found",\
                        "title":"Not Found","status":404,"detail":"Key not found",\
                        "instance":"/v1/service-admin/users/yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs/keys/invalid"}""", result.getBody());

        // Should be able to list keys
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches("\\{\"keys\":\\[]}");

        // list user keys in all services as super admin
        result = restTemplate.exchange(
                "/v1/admin/users/" + keyDto.getOwnerId() + "/keys", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches("\\{\"keys\":\\[]}");

        TestClock.resetToDefaults();
    }

    @Test
    void listApiKeysAcrossAllService_shouldReturnEmptyListWhenUserDoesNotExist() {
        HttpHeaders serviceAdminHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        var result = restTemplate.exchange(
                "/v1/service-admin/users/invalid-user/keys", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals("{\"keys\":[]}", result.getBody());
    }

    @Test
    void adminShouldBeAbleToSuspendUser()
            throws Exception {
        // this test relies on the fact that actions are happening after the token was issued
        Instant testTime = Instant.ofEpochMilli(System.currentTimeMillis())
                .plusSeconds(60);
        TestClock.setBaseClock(TestClock.fixed(testTime, ZoneId.systemDefault()));

        // create key
        HttpHeaders serviceUserHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);
        String testTimeString = DateTimeFormatter.ISO_INSTANT.format(testTime);
        String keyExpiresAtString =
                DateTimeFormatter.ISO_INSTANT.format(testTime.plus(30, ChronoUnit.MINUTES));
        String keyRequest = "{"
                            + " \"expires_at\":\"" + keyExpiresAtString + "\","
                            + " \"description\":\"" + TestData.KEY_DESCRIPTION_1 + "\","
                            + " \"audience_service_ids\":[\"" + TestData.SERVICE_ID_1 + "\"],"
                            + " \"authorizations\":" + TestData.KEY_AUTHZ_1
                            + "}";

        var result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(keyRequest, serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        String responseMatcherKeyCreated =
                "\\{\"id\":\"[a-z0-9-]{36}\","
                + "\"value\":\"nvcfapi-test-[a-zA-Z0-9-_]{64}\","
                + "\"status\":\"ACTIVE\","
                + "\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"issuer_service_id\":\"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "\"audience_service_ids\":\\[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + "\"description\":\"123e4567-description\","
                + "\"created_at\":\"" + testTimeString + "\","
                + "\"expires_at\":\"" + keyExpiresAtString + "\","
                + "\"authorizations\":\\{\"allow\":true}}";
        assertThat(result.getBody()).matches(responseMatcherKeyCreated);
        KeyDto keyDto = jsonMapper.readValue(result.getBody(), KeyDto.class);

        invalidateCaches();

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

        HttpHeaders serviceAdminHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null
        );

        // reading user info
        result = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1, HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(result.getBody()).matches(
                "\\{\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"status\":\"ACTIVE\","
                // match by pattern because this date depends on JWT issue time
                + "\"status_updated_at\":\"\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}Z\"}");

        // Should be able to set USER status to SUSPENDED
        String updateStatusSuspended = "{\"status\":\"SUSPENDED\"}";
        result = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1 + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusSuspended, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals(
                "{\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"status\":\"SUSPENDED\","
                + "\"status_updated_at\":\"" + testTimeString + "\"}", result.getBody());
        // reading user info
        result = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1, HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals(
                "{\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"status\":\"SUSPENDED\","
                + "\"status_updated_at\":\"" + testTimeString + "\"}", result.getBody());

        invalidateCaches();

        // should be unable to make keys
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(keyRequest, serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:forbidden","title":"Forbidden",\
                        "status":403,"detail":"Key owner suspended.","instance":"/v1/keys"}""", result.getBody());

        invalidateCaches();

        // should be unable to introspect key
        result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST,
                new HttpEntity<>(introspectionRequest, headerForIntrospection), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(ERROR_KEY_NOT_FOUND, result.getBody());

        // service admin should be able to list keys of a SUSPENDED user
        result = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys", HttpMethod.GET,
                new HttpEntity<>(serviceAdminHeader), String.class);
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
                + "\"created_at\":\"" + testTimeString + "\","
                + "\"expires_at\":\"" + keyExpiresAtString + "\""
                + "}]}");

        // SUSPENDED user should not be able to list keys
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.GET, new HttpEntity<>(serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:forbidden","title":"Forbidden",\
                        "status":403,"detail":"Key owner suspended.","instance":"/v1/keys"}""", result.getBody());

        // SUSPENDED user should not be able to get key
        result = restTemplate.exchange("/v1/keys/" + keyDto.getId(), HttpMethod.GET,
                                       new HttpEntity<>(serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:forbidden\",\"title\":\"Forbidden\","
                + "\"status\":403,\"detail\":\"Key owner suspended.\","
                + "\"instance\":\"/v1/keys/" + keyDto.getId() + "\"}", result.getBody());

        // SUSPENDED user should not be able to change authorizations
        String updateAuthz = "{\"authorizations\":" + KEY_AUTHZ_2 + "}";
        result = restTemplate.exchange(
                "/v1/keys/" + keyDto.getId() + "/authorizations",
                HttpMethod.PUT, new HttpEntity<>(updateAuthz, serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        assertJsonBodyEquals(
                "{\"type\":\"urn:nv-boot:problem-details:forbidden\",\"title\":\"Forbidden\","
                + "\"status\":403,\"detail\":\"Key owner suspended.\","
                + "\"instance\":\"/v1/keys/" + keyDto.getId() + "/authorizations\"}", result.getBody());

        // Should be able to set USER status to ACTIVE
        String updateStatusActive = "{\"status\":\"ACTIVE\"}";
        result = restTemplate.exchange(
                "/v1/admin/users/" + USER_KEY_OWNER_ID_1 + "/status",
                HttpMethod.PUT, new HttpEntity<>(updateStatusActive, serviceAdminHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals(
                "{\"owner_type\":\"USER\","
                + "\"owner_id\":\"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs\","
                + "\"status\":\"ACTIVE\","
                + "\"status_updated_at\":\"" + testTimeString + "\"}", result.getBody());

        invalidateCaches();

        // should be able to introspect key
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

        // should be able to make keys again
        result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(keyRequest, serviceUserHeaders), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);

        TestClock.resetToDefaults();
    }

    @SneakyThrows
    @Test
    void bulkSuspend_shouldSuspendAllActiveUserKeysInService() {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        HttpHeaders userHeaders = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);
        HttpHeaders serviceAdminHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        // create two ACTIVE keys for the same owner
        var create1 = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(KEY_REQUEST_1, userHeaders), String.class);
        assertThat(create1.getStatusCode()).isEqualTo(HttpStatus.OK);
        KeyDto key1 = jsonMapper.readValue(create1.getBody(), KeyDto.class);

        var create2 = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST,
                new HttpEntity<>(KEY_REQUEST_2, userHeaders), String.class);
        assertThat(create2.getStatusCode()).isEqualTo(HttpStatus.OK);
        KeyDto key2 = jsonMapper.readValue(create2.getBody(), KeyDto.class);

        // bulk-suspend
        var suspendResult = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/suspend",
                HttpMethod.PUT, new HttpEntity<>("{}", serviceAdminHeader), String.class);
        assertThat(suspendResult.getStatusCode()).isEqualTo(HttpStatus.OK);

        ListKeysResponse suspendBody = jsonMapper.readValue(
                suspendResult.getBody(), ListKeysResponse.class);
        assertThat(suspendBody.getKeys())
                .extracting(KeyDto::getId, KeyDto::getStatus)
                .containsExactlyInAnyOrder(
                        tuple(key1.getId(), KeyStatus.SUSPENDED),
                        tuple(key2.getId(), KeyStatus.SUSPENDED));

        // verify via list-keys: both rows reflect SUSPENDED
        var listResult = restTemplate.exchange(
                "/v1/service-admin/users/" + USER_KEY_OWNER_ID_1 + "/keys",
                HttpMethod.GET, new HttpEntity<>(serviceAdminHeader), String.class);
        assertThat(listResult.getStatusCode()).isEqualTo(HttpStatus.OK);
        ListKeysResponse listBody = jsonMapper.readValue(
                listResult.getBody(), ListKeysResponse.class);
        assertThat(listBody.getKeys())
                .extracting(KeyDto::getStatus)
                .containsOnly(KeyStatus.SUSPENDED);

        TestClock.resetToDefaults();
    }

    @SneakyThrows
    @Test
    void bulkSuspend_shouldReturnEmptyListWhenUserHasNoKeys() {
        HttpHeaders serviceAdminHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, SERVICE_ADMIN_USER_ID, null);

        var result = restTemplate.exchange(
                "/v1/service-admin/users/no-such-user/suspend",
                HttpMethod.PUT, new HttpEntity<>("{}", serviceAdminHeader), String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);

        ListKeysResponse body = jsonMapper.readValue(result.getBody(), ListKeysResponse.class);
        assertThat(body.getKeys()).isEmpty();
    }
}
