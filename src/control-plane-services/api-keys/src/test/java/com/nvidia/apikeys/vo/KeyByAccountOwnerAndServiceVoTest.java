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

package com.nvidia.apikeys.vo;

import static com.nvidia.apikeys.TestData.KEY_VO_1;
import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.Test;

class KeyByAccountOwnerAndServiceVoTest {

    @Test
    void fromCopiesEveryManagementFieldFromKey() {
        KeyVo key = KEY_VO_1.toBuilder().ncaId("nca-1").build();

        KeyByAccountOwnerAndServiceVo vo = KeyByAccountOwnerAndServiceVo.from(key);

        assertThat(vo.getNcaId()).isEqualTo("nca-1");
        assertThat(vo.getOwnerType()).isEqualTo(key.getOwnerType());
        assertThat(vo.getOwnerId()).isEqualTo(key.getOwnerId());
        assertThat(vo.getIssuerServiceId()).isEqualTo(key.getIssuerServiceId());
        assertThat(vo.getKeyId()).isEqualTo(key.getKeyId());
        assertThat(vo.getCreatedAt()).isEqualTo(key.getCreatedAt());
        assertThat(vo.getExpiresAt()).isEqualTo(key.getExpiresAt());
        assertThat(vo.getDeletesAt()).isEqualTo(key.getDeletesAt());
        assertThat(vo.getKeyStatus()).isEqualTo(key.getKeyStatus());
        assertThat(vo.getKeyHash()).isEqualTo(key.getKeyHash());
        assertThat(vo.getApiKeySuffix()).isEqualTo(key.getApiKeySuffix());
        assertThat(vo.getDescription()).isEqualTo(key.getDescription());
        assertThat(vo.getAudienceServiceIds()).isEqualTo(key.getAudienceServiceIds());
    }

    @Test
    void fromKeepsNullAccount() {
        assertThat(KeyByAccountOwnerAndServiceVo.from(KEY_VO_1).getNcaId()).isNull();
    }
}
