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


package com.nvidia.apikeys.converters;

import static com.nvidia.apikeys.TestData.API_KEY_1;
import static com.nvidia.apikeys.TestData.API_KEY_SUFFIX_1;
import static com.nvidia.apikeys.TestData.GENERATED_KEY_VO_1;
import static com.nvidia.apikeys.TestData.KEY_AUTHZ_1;
import static com.nvidia.apikeys.TestData.KEY_EXPIRES_AT_1;
import static com.nvidia.apikeys.TestData.KEY_ID_1;
import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.dto.keysV2.KeyStatusV2;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountAndServiceModel;
import com.nvidia.apikeys.persistance.models.OwnerStatusByAccountModel;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.utils.TestClock;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.apikeys.vo.OwnerAccountStatusVo;
import java.time.Instant;
import java.time.ZoneId;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import tools.jackson.databind.json.JsonMapper;

class KeyV2ResponseBuilderTest {

    private static final JsonMapper JSON_MAPPER = new JsonMapper();

    private final KeyV2ResponseBuilder builder = new KeyV2ResponseBuilder(
            JSON_MAPPER,
            new MillisecondPrecisionClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault())));

    @Test
    void toCreateResponseCarriesFullKeyValue() {
        var response = builder.toCreateResponse(KEY_V2_VO_1, GENERATED_KEY_VO_1);

        assertThat(response.getValue()).isEqualTo(API_KEY_1);
        assertThat(response.getId()).isEqualTo(KEY_ID_1);
        assertThat(response.getAuthorizations()).isEqualTo(JSON_MAPPER.readTree(KEY_AUTHZ_1));
    }

    @Test
    void toResponseCarriesMaskedValueAndAuthorizations() {
        var response = builder.toResponse(KEY_V2_VO_1);

        assertThat(response.getValue()).isEqualTo(API_KEY_SUFFIX_1);
        assertThat(response.getId()).isEqualTo(KEY_ID_1);
        assertThat(response.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(response.getAccountId()).isEqualTo(NCA_ID_1);
        assertThat(response.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
        assertThat(response.getStatus()).isEqualTo(KeyStatusV2.ACTIVE);
        assertThat(response.getExpiresAt()).isEqualTo(KEY_EXPIRES_AT_1);
        assertThat(response.getAuthorizations()).isEqualTo(JSON_MAPPER.readTree(KEY_AUTHZ_1));
    }

    @Test
    void toListItemOmitsAuthorizations() {
        var response = builder.toListItem(KeysByOwnerAndAccountAndServiceVo.from(KEY_V2_VO_1));

        assertThat(response.getAuthorizations()).isNull();
        assertThat(response.getValue()).isEqualTo(API_KEY_SUFFIX_1);
        assertThat(response.getAccountId()).isEqualTo(NCA_ID_1);
    }

    @ParameterizedTest
    @CsvSource({
            "ACTIVE, 2023-10-04T08:25:24Z, ACTIVE",
            "SUSPENDED, 2023-10-04T08:25:24Z, SUSPENDED",
            "REVOKED, 2023-10-04T08:25:24Z, REVOKED",
            "ACTIVE, 2023-10-03T08:25:23Z, EXPIRED",
            "ACTIVE, 2023-10-03T08:25:24Z, EXPIRED",
            "SUSPENDED, 2023-10-03T08:25:23Z, EXPIRED",
            "REVOKED, 2023-10-03T08:25:23Z, REVOKED",
            "EXPIRED, 2023-10-04T08:25:24Z, EXPIRED",
    })
    void statusIsDerivedFromStoredStatusAndExpiry(
            KeyStatus stored, Instant expiresAt, KeyStatusV2 expected) {
        var key = KEY_V2_VO_1.toBuilder().keyStatus(stored).expiresAt(expiresAt).build();

        assertThat(builder.toResponse(key).getStatus()).isEqualTo(expected);
        assertThat(builder.toListItem(KeysByOwnerAndAccountAndServiceVo.from(key)).getStatus())
                .isEqualTo(expected);
    }

    @Test
    void toListResponseCarriesLimitAndCursor() {
        var response = builder.toListResponse(
                List.of(KeysByOwnerAndAccountAndServiceVo.from(KEY_V2_VO_1)), 10, "next");

        assertThat(response.getKeys()).singleElement()
                .satisfies(key -> assertThat(key.getId()).isEqualTo(KEY_ID_1));
        assertThat(response.getLimit()).isEqualTo(10);
        assertThat(response.getCursor()).isEqualTo("next");
    }

    @Test
    void toStatusResponseForAccountOmitsIssuerService() {
        var model = OwnerStatusByAccountModel.builder()
                .ncaId(NCA_ID_1)
                .ownerType(KeyOwnerType.USER)
                .ownerId(USER_KEY_OWNER_ID_1)
                .ownerStatus(KeyOwnerStatus.SUSPENDED)
                .updatedAt(TEST_TIME)
                .build();

        var response = builder.toStatusResponse(model);

        assertThat(response.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(response.getAccountId()).isEqualTo(NCA_ID_1);
        assertThat(response.getIssuerServiceId()).isNull();
        assertThat(response.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
        assertThat(response.getUpdatedAt()).isEqualTo(TEST_TIME);
    }

    @Test
    void toStatusResponseForIssuerServiceCarriesIssuerService() {
        var model = OwnerStatusByAccountAndServiceModel.builder()
                .ncaId(NCA_ID_1)
                .ownerType(KeyOwnerType.USER)
                .ownerId(USER_KEY_OWNER_ID_1)
                .issuerServiceId(SERVICE_ID_1)
                .ownerStatus(KeyOwnerStatus.SUSPENDED)
                .updatedAt(TEST_TIME)
                .build();

        var response = builder.toStatusResponse(model);

        assertThat(response.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
        assertThat(response.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
    }

    @Test
    void toStatusesResponseMapsAccountsAndIssuerServices() {
        var withIssuer = OwnerAccountStatusVo.builder()
                .ncaId(NCA_ID_1)
                .status(KeyOwnerStatus.ACTIVE)
                .issuerServices(List.of(new OwnerAccountStatusVo.IssuerStatus(
                        SERVICE_ID_1, KeyOwnerStatus.SUSPENDED, TEST_TIME)))
                .build();
        var withoutIssuer = OwnerAccountStatusVo.builder()
                .ncaId("nca-2")
                .status(KeyOwnerStatus.SUSPENDED)
                .issuerServices(List.of())
                .build();

        var response = builder.toStatusesResponse(
                USER_KEY_OWNER_ID_1, List.of(withIssuer, withoutIssuer), 5, "next");

        assertThat(response.getOwnerId()).isEqualTo(USER_KEY_OWNER_ID_1);
        assertThat(response.getLimit()).isEqualTo(5);
        assertThat(response.getCursor()).isEqualTo("next");
        assertThat(response.getAccounts()).hasSize(2);
        assertThat(response.getAccounts().get(0).getAccountId()).isEqualTo(NCA_ID_1);
        assertThat(response.getAccounts().get(0).getIssuerServices()).singleElement()
                .satisfies(issuer -> {
                    assertThat(issuer.getIssuerServiceId()).isEqualTo(SERVICE_ID_1);
                    assertThat(issuer.getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
                    assertThat(issuer.getUpdatedAt()).isEqualTo(TEST_TIME);
                });
        assertThat(response.getAccounts().get(1).getStatus()).isEqualTo(KeyOwnerStatus.SUSPENDED);
    }
}
