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

package com.nvidia.apikeys.services;

import static com.nvidia.apikeys.TestData.KEY_BY_OWNER_AND_SERVICE_VO_1;
import static com.nvidia.apikeys.TestData.KEY_V2_VO_1;
import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static com.nvidia.apikeys.TestData.NCA_ID_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.utils.TestUtils.assertThrowsExceptionWithDetails;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.vo.KeyV2Vo;
import com.nvidia.apikeys.vo.KeysByOwnerAndAccountAndServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.Test;

class LegacyAccountTranslatorTest {

    static final String LEGACY_NCA_ID = "legacy-nca";
    static final String SERVICE_NAME = "nvcf";
    static final Map<String, String> SERVICE_ID_MAP = Map.of(
            SERVICE_NAME, SERVICE_ID_1,
            "nvct", "legacy-nvct-service-id");

    static final KeyV2Vo LEGACY_KEY_V2 = KEY_V2_VO_1.toBuilder()
            .ncaId(LEGACY_NCA_ID)
            .issuerServiceId(SERVICE_NAME)
            .audienceServiceIds(Set.of(SERVICE_NAME))
            .build();

    private final LegacyAccountTranslator translator = new LegacyAccountTranslator(
            NakProperties.builder().ncaId(LEGACY_NCA_ID).serviceIdMap(SERVICE_ID_MAP).build());

    @Test
    void onlyTheConfiguredAccountIsLegacy() {
        assertThat(translator.isLegacy(LEGACY_NCA_ID)).isTrue();
        assertThat(translator.isLegacy(NCA_ID_1)).isFalse();
        assertThat(translator.isLegacy(null)).isFalse();
    }

    @Test
    void onlyMappedIssuerServiceIdsHaveLegacyIds() {
        assertThat(translator.findLegacyIssuerServiceId(SERVICE_NAME)).contains(SERVICE_ID_1);
        assertThat(translator.findLegacyIssuerServiceId(SERVICE_ID_1)).isEmpty();
    }

    @Test
    void unmappedLegacyServiceIdIsKept() {
        assertThat(translator.toServiceId(SERVICE_ID_1)).isEqualTo(SERVICE_NAME);
        assertThat(translator.toServiceId("other-service")).isEqualTo("other-service");
    }

    @Test
    void toLegacyTranslatesServiceIdsAndDropsNcaId() {
        assertThat(translator.toLegacy(LEGACY_KEY_V2)).isEqualTo(KEY_VO_1);
    }

    @Test
    void toLegacyKeepsUnmappedAudiences() {
        var key = LEGACY_KEY_V2.toBuilder()
                .audienceServiceIds(Set.of(SERVICE_NAME, "other-service"))
                .build();

        assertThat(translator.toLegacy(key).getAudienceServiceIds())
                .containsExactlyInAnyOrder(SERVICE_ID_1, "other-service");
    }

    @Test
    void toLegacyRejectsUnmappedIssuer() {
        var key = LEGACY_KEY_V2.toBuilder().issuerServiceId("other-service").build();

        assertThrowsExceptionWithDetails(
                BadRequestException.class,
                () -> translator.toLegacy(key),
                "Unknown issuer service: 'other-service'");
    }

    @Test
    void fromLegacyKeyAddsLegacyNcaIdAndTranslatesServiceIds() {
        assertThat(translator.fromLegacy(KEY_VO_1)).isEqualTo(LEGACY_KEY_V2);
    }

    @Test
    void fromLegacyKeyByOwnerAndServiceAddsLegacyNcaId() {
        assertThat(translator.fromLegacy(KEY_BY_OWNER_AND_SERVICE_VO_1))
                .isEqualTo(KeysByOwnerAndAccountAndServiceVo.from(LEGACY_KEY_V2));
    }

    @Test
    void nullAudiencesStayNull() {
        var key = KEY_VO_1.toBuilder().audienceServiceIds(null).build();

        assertThat(translator.fromLegacy(key).getAudienceServiceIds()).isNull();
        assertThat(translator.toLegacy(translator.fromLegacy(key)).getAudienceServiceIds())
                .isNull();
    }
}
