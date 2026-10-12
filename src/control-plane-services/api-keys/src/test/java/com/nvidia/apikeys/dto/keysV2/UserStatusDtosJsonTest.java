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

package com.nvidia.apikeys.dto.keysV2;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.utils.JsonUtils;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import java.time.Instant;
import java.util.List;
import org.junit.jupiter.api.Test;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

class UserStatusDtosJsonTest {

    private final JsonMapper mapper = JsonUtils.getRequestResponseJsonMapper();

    @Test
    void accountRequest_deserializesStatusAndReason() throws Exception {
        UpdateUserStatusForAccountRequest request = mapper.readValue("""
                {"status": "SUSPENDED", "reason": "User access suspended in this account"}
                """, UpdateUserStatusForAccountRequest.class);

        assertThat(request.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(request.getReason()).isEqualTo("User access suspended in this account");
    }

    @Test
    void accountAndIssuerRequest_deserializesStatus() throws Exception {
        UpdateUserStatusForAccountAndIssuerServiceRequest request = mapper.readValue(
                "{\"status\":\"ACTIVE\"}", UpdateUserStatusForAccountAndIssuerServiceRequest.class);

        assertThat(request.getStatus()).isEqualTo(KeyOwnerStatus.ACTIVE);
        assertThat(request.getReason()).isNull();
    }

    @Test
    void userStatusResponse_omitsIssuerForAccountScope() throws Exception {
        UserStatusResponse response = UserStatusResponse.builder()
                .userId("user-1")
                .ncaId("account-a")
                .status(KeyOwnerStatus.SUSPENDED)
                .updatedAt(Instant.parse("2026-09-01T00:00:00Z"))
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("userId").asString()).isEqualTo("user-1");
        assertThat(json.get("ncaId").asString()).isEqualTo("account-a");
        assertThat(json.get("status").asString()).isEqualTo("SUSPENDED");
        assertThat(json.has("issuerServiceId")).isFalse();
    }

    @Test
    void userStatusResponse_includesIssuerForIssuerScope() throws Exception {
        UserStatusResponse response = UserStatusResponse.builder()
                .userId("user-1")
                .ncaId("account-a")
                .issuerServiceId("cloud-functions")
                .status(KeyOwnerStatus.SUSPENDED)
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("issuerServiceId").asString()).isEqualTo("cloud-functions");
    }

    @Test
    void userStatusesResponse_roundTripsNestedAccountsAndIssuers() throws Exception {
        UserStatusesResponse response = UserStatusesResponse.builder()
                .userId("user-1")
                .accounts(List.of(
                        AccountUserStatus.builder()
                                .ncaId("account-a")
                                .status(KeyOwnerStatus.ACTIVE)
                                .issuerServices(List.of(IssuerUserStatus.builder()
                                        .issuerServiceId("cloud-functions")
                                        .status(KeyOwnerStatus.SUSPENDED)
                                        .updatedAt(Instant.parse("2026-09-01T00:00:00Z"))
                                        .build()))
                                .build(),
                        AccountUserStatus.builder()
                                .ncaId("account-b")
                                .status(KeyOwnerStatus.SUSPENDED)
                                .build()))
                .limit(10)
                .cursor("opaque-cursor")
                .build();

        UserStatusesResponse parsed = mapper.readValue(
                mapper.writeValueAsString(response), UserStatusesResponse.class);

        assertThat(parsed).isEqualTo(response);
    }

    @Test
    void userStatusesResponse_omitsEmptyIssuerServicesAndCursor() throws Exception {
        UserStatusesResponse response = UserStatusesResponse.builder()
                .userId("user-1")
                .accounts(List.of(AccountUserStatus.builder()
                        .ncaId("account-a")
                        .status(KeyOwnerStatus.ACTIVE)
                        .build()))
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.has("cursor")).isFalse();
        assertThat(json.has("limit")).isFalse();
        assertThat(json.get("accounts").get(0).has("issuerServices")).isFalse();
    }
}
