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

import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import com.nvidia.apikeys.dto.keysV2.KeyOperationResponse;
import com.nvidia.apikeys.dto.keysV2.KeysOperationType;
import com.nvidia.apikeys.persistance.dao.KeysV2Dao;
import com.nvidia.apikeys.persistance.dao.OwnerStatusByAccountDao;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOperationStatus;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyV2Vo;
import java.time.Clock;
import java.time.ZoneOffset;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.junit.jupiter.params.provider.ValueSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(classes = App.class, webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {"spring.profiles.active=integrationtest", "spring.cassandra.request.page-size=2"})
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class SyncKeysV2OperationsControllerTest extends BaseIntegrationTest {

    private static final String OWNER_ROUTE = "/v2/api-keys/key-owner/users/user*/key-operations";
    private static final String ADMIN_ROUTE = "/v2/api-keys/account-admin/accounts/a*/key-operations";
    private static final String PLATFORM_ROUTE = "/v2/api-keys/platform-admin/key-operations";

    @Autowired
    private KeysV2Dao keys;
    @Autowired
    private OwnerStatusByAccountDao statuses;

    @BeforeEach
    void setUpClock() {
        TestClock.setBaseClock(Clock.fixed(TEST_TIME, ZoneOffset.UTC));
    }

    @AfterEach
    void resetClock() {
        TestClock.resetToDefaults();
    }

    @ParameterizedTest
    @CsvSource({"owner,REVOKED", "owner,DELETE", "account,REVOKED", "account,DELETE",
            "platform,REVOKED", "platform,DELETE"})
    void completesWithinActorScopeAndCutoff(String actor, KeysOperationType operation) {
        var seeded = seedKeys();
        var route = switch (actor) {
            case "owner" -> OWNER_ROUTE;
            case "account" -> ADMIN_ROUTE;
            default -> PLATFORM_ROUTE;
        };
        // Unknown fields cannot replace the dimension fixed by the route or the internal USER type.
        var body = switch (actor) {
            case "owner" -> Map.of("operation", operation, "accounts", List.of("*"),
                    "issuerServices", List.of("nvcf"), "users", List.of("u2"), "ownerType", "SERVICE");
            case "account" -> Map.of("operation", operation, "users", List.of("*"),
                    "issuerServices", List.of("*"), "accounts", List.of("b"), "ownerType", "SERVICE");
            default -> Map.of("operation", operation, "accounts", List.of("a*", "b"),
                    "users", List.of("user*", "u2"), "issuerServices", List.of("nvcf"));
        };

        var response = put(route, body);

        long completed = 0;
        for (var key : seeded) {
            var selected = !key.getCreatedAt().isAfter(TEST_TIME) && switch (actor) {
                case "owner" -> key.getOwnerId().equals("user*") && key.getIssuerServiceId().equals("nvcf");
                case "account" -> key.getNcaId().equals("a*");
                default -> key.getIssuerServiceId().equals("nvcf");
            };
            assertStoredState(key, selected ? operation : null);
            if (selected) {
                completed++;
            }
        }
        assertCompleted(response, operation, completed);
    }

    @ParameterizedTest
    @ValueSource(strings = {"missing", "false", "true"})
    void requiresConfirmationForAllWildcardPlatformRequest(String confirmation) {
        var seeded = seedKeys();
        var body = jsonMapper.createObjectNode()
                .put("operation", "REVOKED");
        body.putArray("accounts").add("*");
        body.putArray("users").add("*");
        body.putArray("issuerServices").add("*");
        if (!confirmation.equals("missing")) {
            body.put("confirm_platform_wide", Boolean.parseBoolean(confirmation));
        }

        var response = put(PLATFORM_ROUTE, body);

        if (confirmation.equals("true")) {
            assertCompleted(response, KeysOperationType.REVOKED, 8);
        } else {
            assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
            seeded.forEach(key -> assertStoredState(key, null));
        }
    }

    @ParameterizedTest
    @ValueSource(strings = {"{", "{\"operation\":\"REVOKED\"}",
            "{\"operation\":\"SUSPENDED\",\"accounts\":[\"*\"],\"issuerServices\":[\"*\"]}",
            "{\"operation\":\"REVOKED\",\"accounts\":[\"*\",\"a*\"],\"issuerServices\":[\"*\"]}"})
    void rejectsInvalidHttpRequestsBeforeMutation(String body) {
        var seeded = seedKeys();

        assertThat(put(OWNER_ROUTE, body).getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);

        seeded.forEach(key -> assertStoredState(key, null));
    }

    @ParameterizedTest
    @ValueSource(strings = {"owner", "account"})
    void rejectsReservedTokenInFixedRoute(String actor) {
        var seeded = seedKeys();
        var response = actor.equals("owner")
                ? put(OWNER_ROUTE.replace("user*", "*"), Map.of("operation", "DELETE",
                        "accounts", List.of("*"), "issuerServices", List.of("*")))
                : put(ADMIN_ROUTE.replace("a*", "*"), Map.of("operation", "DELETE",
                        "users", List.of("*"), "issuerServices", List.of("*")));

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
        seeded.forEach(key -> assertStoredState(key, null));
    }

    @ParameterizedTest
    @ValueSource(booleans = {false, true})
    void deniesSuspendedOwnerWhileAllowingAccountAdmin(boolean issuerScoped) {
        var seeded = seedKeys();
        if (issuerScoped) {
            statuses.saveOwnerStatusByAccountAndService("a*", USER, "user*", "nvcf", KeyOwnerStatus.SUSPENDED);
        } else {
            statuses.saveOwnerStatusByAccount("a*", USER, "user*", KeyOwnerStatus.SUSPENDED);
        }
        var response = put(OWNER_ROUTE, Map.of("operation", "REVOKED", "accounts", List.of("*"),
                "issuerServices", List.of("*")));

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.FORBIDDEN);
        seeded.forEach(key -> assertStoredState(key, null));
        assertCompleted(put(ADMIN_ROUTE, Map.of("operation", "REVOKED", "users", List.of("user*"),
                "issuerServices", List.of("*"))), KeysOperationType.REVOKED, 2);
    }

    @Test
    void emptySelectionReturnsCompletedOperationWithGeneratedId() {
        var body = Map.of("operation", "DELETE", "accounts", List.of("a*"), "users", List.of("user*"),
                "issuerServices", List.of("nvcf"), "operationId", "00000000-0000-0000-0000-000000000000");

        var response = put(PLATFORM_ROUTE, body);

        assertCompleted(response, KeysOperationType.DELETE, 0);
        assertThat(jsonMapper.readValue(response.getBody(), KeyOperationResponse.class).getOperationId())
                .asString().isNotEqualTo("00000000-0000-0000-0000-000000000000");
    }

    private ResponseEntity<String> put(String route, Object body) {
        var headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        headers.set("NVCF-ACTOR-ID", "bulk-test-actor");
        return restTemplate.exchange(route, HttpMethod.PUT, new HttpEntity<>(body, headers), String.class);
    }

    private void assertCompleted(ResponseEntity<String> response, KeysOperationType operation, long count) {
        assertThat(response.getStatusCode()).as(response.getBody()).isEqualTo(HttpStatus.OK);
        assertThat(response.getHeaders().getLocation()).isNull();
        var result = jsonMapper.readValue(response.getBody(), KeyOperationResponse.class);
        assertThat(result.getOperationId()).isNotNull();
        assertThat(result.getOperation()).isEqualTo(operation);
        assertThat(result.getStatus()).isEqualTo(KeyOperationStatus.COMPLETED);
        assertThat(result.getMatchedCount()).isEqualTo(count);
        assertThat(result.getCompletedCount()).isEqualTo(count);
        assertThat(result.getFailedCount()).isZero();
        assertThat(result.getSubmittedAt()).isEqualTo(TEST_TIME);
        assertThat(result.getUpdatedAt()).isEqualTo(TEST_TIME);
        assertThat(jsonMapper.readTree(response.getBody()).has("ownerType")).isFalse();
    }

    private List<KeyV2Vo> seedKeys() {
        var seeded = new ArrayList<KeyV2Vo>();
        for (var account : List.of("a*", "b")) {
            for (var user : List.of("user*", "u2")) {
                for (var issuer : List.of("nvcf", "nvct")) {
                    var id = account + "-" + user + "-" + issuer;
                    seeded.add(KEY_V2_VO_1.toBuilder().ncaId(account).ownerId(user).issuerServiceId(issuer)
                            .audienceServiceIds(Set.of(issuer)).keyId(id).keyHash(id + "-hash").build());
                }
            }
        }
        seeded.add(seeded.getFirst().toBuilder().keyId("future").keyHash("future-hash")
                .createdAt(TEST_TIME.plusMillis(1)).build());
        seeded.forEach(keys::save);
        return seeded;
    }

    private void assertStoredState(KeyV2Vo key, KeysOperationType operation) {
        var lookup = keys.getKeyByHash(key.getKeyHash());
        var management = keys.get(key.getNcaId(), USER, key.getOwnerId(), key.getIssuerServiceId(), key.getKeyId());
        if (operation == KeysOperationType.DELETE) {
            assertThat(lookup).isEmpty();
            assertThat(management).isEmpty();
        } else {
            var status = operation == KeysOperationType.REVOKED ? KeyStatus.REVOKED : KeyStatus.ACTIVE;
            assertThat(lookup).get().extracting(KeyV2Vo::getKeyStatus).isEqualTo(status);
            assertThat(management).get().extracting(value -> value.getKeyStatus()).isEqualTo(status);
        }
    }
}
