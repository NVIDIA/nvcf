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

class KeyOwnerStatusDtosJsonTest {

    private final JsonMapper mapper = JsonUtils.getRequestResponseJsonMapper();

    @Test
    void accountRequest_deserializesStatusAndReason() throws Exception {
        UpdateOwnerStatusForAccountRequest request = mapper.readValue("""
                {"status": "SUSPENDED", "reason": "User access suspended in this account"}
                """, UpdateOwnerStatusForAccountRequest.class);

        assertThat(request.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(request.getReason()).isEqualTo("User access suspended in this account");
    }

    @Test
    void accountAndIssuerRequest_deserializesStatus() throws Exception {
        UpdateOwnerStatusForAccountAndIssuerServiceRequest request = mapper.readValue(
                "{\"status\":\"ACTIVE\"}", UpdateOwnerStatusForAccountAndIssuerServiceRequest.class);

        assertThat(request.getStatus()).isEqualTo(KeyOwnerStatus.ACTIVE);
        assertThat(request.getReason()).isNull();
    }

    @Test
    void ownerStatusResponse_omitsIssuerForAccountScope() throws Exception {
        KeyOwnerStatusResponse response = KeyOwnerStatusResponse.builder()
                .ownerId("user-1")
                .accountId("account-a")
                .status(KeyOwnerStatus.SUSPENDED)
                .updatedAt(Instant.parse("2026-09-01T00:00:00Z"))
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("ownerId").asString()).isEqualTo("user-1");
        assertThat(json.get("accountId").asString()).isEqualTo("account-a");
        assertThat(json.get("status").asString()).isEqualTo("SUSPENDED");
        assertThat(json.has("issuerServiceId")).isFalse();
    }

    @Test
    void ownerStatusResponse_includesIssuerForIssuerScope() throws Exception {
        KeyOwnerStatusResponse response = KeyOwnerStatusResponse.builder()
                .ownerId("user-1")
                .accountId("account-a")
                .issuerServiceId("cloud-functions")
                .status(KeyOwnerStatus.SUSPENDED)
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.get("issuerServiceId").asString()).isEqualTo("cloud-functions");
    }

    @Test
    void ownerStatusesResponse_roundTripsNestedAccountsAndIssuers() throws Exception {
        KeyOwnerStatusesResponse response = KeyOwnerStatusesResponse.builder()
                .ownerId("user-1")
                .accounts(List.of(
                        OwnerStatusByAccount.builder()
                                .accountId("account-a")
                                .status(KeyOwnerStatus.ACTIVE)
                                .issuerServices(List.of(OwnerStatusByService.builder()
                                        .issuerServiceId("cloud-functions")
                                        .status(KeyOwnerStatus.SUSPENDED)
                                        .updatedAt(Instant.parse("2026-09-01T00:00:00Z"))
                                        .build()))
                                .build(),
                        OwnerStatusByAccount.builder()
                                .accountId("account-b")
                                .status(KeyOwnerStatus.SUSPENDED)
                                .build()))
                .limit(10)
                .cursor("opaque-cursor")
                .build();

        KeyOwnerStatusesResponse parsed = mapper.readValue(
                mapper.writeValueAsString(response), KeyOwnerStatusesResponse.class);

        assertThat(parsed).isEqualTo(response);
    }

    @Test
    void ownerStatusesResponse_omitsEmptyIssuerServicesAndCursor() throws Exception {
        KeyOwnerStatusesResponse response = KeyOwnerStatusesResponse.builder()
                .ownerId("user-1")
                .accounts(List.of(OwnerStatusByAccount.builder()
                        .accountId("account-a")
                        .status(KeyOwnerStatus.ACTIVE)
                        .build()))
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(response));

        assertThat(json.has("cursor")).isFalse();
        assertThat(json.has("limit")).isFalse();
        assertThat(json.get("accounts").get(0).has("issuerServices")).isFalse();
    }

    @Test
    void ownerStatusByAccount_omitsIssuerServicesWhenListIsEmpty() throws Exception {
        OwnerStatusByAccount status = OwnerStatusByAccount.builder()
                .accountId("account-a")
                .status(KeyOwnerStatus.ACTIVE)
                .issuerServices(List.of())
                .build();

        JsonNode json = mapper.readTree(mapper.writeValueAsString(status));

        assertThat(json.has("issuerServices")).isFalse();
    }
}
