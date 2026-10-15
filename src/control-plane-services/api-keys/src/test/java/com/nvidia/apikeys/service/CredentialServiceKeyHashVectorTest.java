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

package com.nvidia.apikeys.service;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.config.hmac.HmacEncoder;
import com.nvidia.apikeys.config.hmac.HmacWorkersPooledFactory;
import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.apikeys.validators.NakPropertiesValidator;
import java.time.Duration;
import java.util.Map;
import org.apache.commons.pool2.impl.GenericKeyedObjectPool;
import org.apache.commons.pool2.impl.GenericKeyedObjectPoolConfig;
import org.junit.jupiter.api.Test;

/**
 * Upgrade-compatibility regression vector: pins {@code CredentialService.getKeyHash}
 * to the exact base64url string the pre-migration build produced for a fixed
 * {@code (dataDomainKey, plaintextKey)} pair. The expected value was computed
 * independently in Python (hashlib.sha3_256 + hmac stdlib) so the test cross-validates
 * the encoder + base64url-encoding pipeline against a reference implementation that
 * shares no code with BC, the JDK, or commons-pool2.
 */
class CredentialServiceKeyHashVectorTest {

    private static final String DATA_DOMAIN_KEY_B64URL =
            "KVyrsdeNCW5IXxFgysCLN35sir4Uqh4ZuWUZmv9pBHRdIcUOTZ79JfMLDZlKEvPhKrVHZX-ZP1jpGMrxsKjfEXIi"
            + "Y_APV0dn-fp0mQBvC-GwbAot_w7ztxxXGrYX2vVcC5eGqpTR1x3up_OZHkMy6bfF731Qn_kZzOWOMNBWfBaU"
            + "4_l2wbkolg";
    // 32-byte api-key plaintext (0x00..0x1F), base64url-encoded without padding
    private static final String PLAINTEXT_KEY_B64URL = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8";
    private static final String EXPECTED_KEY_HASH = "0QhgY_pB2f54B2j5JnM3funskjoIe0xkbIKehpDlblo";

    @Test
    void getKeyHash_matchesPreMigrationVector() {
        HmacEncoder encoder = new HmacEncoder(new GenericKeyedObjectPool<>(
                new HmacWorkersPooledFactory(), new GenericKeyedObjectPoolConfig<>()));
        NakProperties config = NakProperties.builder()
                .dataDomainKey(DATA_DOMAIN_KEY_B64URL)
                .keyPrefix("nvcfapi-test-")
                .keepAfterExpiredDuration(Duration.ofDays(2))
                .ncaId("test-nca-id")
                .registrations("[]")
                .serviceIdMap(Map.of("nvcf", "service-1"))
                .build();
        CredentialService service = new CredentialService(
                encoder, new NakPropertiesValidator(), config, null, null);

        assertThat(service.getKeyHash(PLAINTEXT_KEY_B64URL)).isEqualTo(EXPECTED_KEY_HASH);
    }
}
