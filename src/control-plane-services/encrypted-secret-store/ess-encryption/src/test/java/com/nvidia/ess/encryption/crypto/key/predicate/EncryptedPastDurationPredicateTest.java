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
package com.nvidia.ess.encryption.crypto.key.predicate;

import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;
import java.util.stream.Stream;
import org.apache.commons.lang3.RandomStringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

class EncryptedPastDurationPredicateTest {

    private static Stream<Arguments> args() {
        return Stream.of(
                Arguments.of(Duration.ZERO, CryptoTestUtils.generateMasterEncryptionKey().getKeyID(), RandomStringUtils.secure().nextAlphanumeric(25), true),
                Arguments.of(Duration.ZERO, CryptoTestUtils.generateMasterEncryptionKey().getKeyID(), UUID.randomUUID().toString(), true),
                Arguments.of(Duration.ZERO, CryptoTestUtils.generateMasterEncryptionKey().getKeyID(), CryptoTestUtils.generateMasterEncryptionKey(Instant.now().plus(Duration.ofDays(10)).toEpochMilli()).getKeyID(), false),
                Arguments.of(Duration.ofDays(10), CryptoTestUtils.generateMasterEncryptionKey().getKeyID(), CryptoTestUtils.generateMasterEncryptionKey(Instant.now().minus(Duration.ofDays(1)).toEpochMilli()).getKeyID(), false)
        );
    }

    @ParameterizedTest
    @MethodSource("args")
    void shouldRun_onInvalidEncryptedByKid_returnsFalse(Duration duration, String mekKid, String encryptedByKid, boolean expectedResult) {
        var model = new EncryptionKeyModel();
        model.setEncryptedByKid(encryptedByKid);
        model.setEncryptedAt(Instant.now());

        Assertions.assertEquals(expectedResult, new EncryptedPastDurationPredicate(duration, mekKid).shouldRun(model));
    }
}
