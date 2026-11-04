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
package com.nvidia.notary.services;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.KeyUse;
import com.nimbusds.jose.jwk.gen.ECKeyGenerator;
import java.util.Date;
import java.util.UUID;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;

@Slf4j
public class KeyGeneratorTest {

    /**
     * Use this test to generate signing keys.
     */
    @Test
    public void generateSigningKey() {
        ECKey jwk = makeNewEcKey();
        // Output the private key
        System.out.println(jwk);
    }

    /**
     * Use this test to generate key set.
     */
    @Test
    public void generateInitialKeySet() {
        ECKey jwk = makeNewEcKey();
        System.out.printf("{\"keys\":[%s]}%n", jwk);
    }

    /**
     * Use this test to generate key set.
     */
    @Test
    public void generateInitialKeySetEscaped() {
        ECKey jwk = makeNewEcKey();
        String keySet = "{\"keys\":[%s]}%n".formatted(jwk);
        System.out.print(keySet.replaceAll("\"", "\\\\\\\\\\\\\""));
    }

    @SneakyThrows
    private static ECKey makeNewEcKey() {
        return new ECKeyGenerator(Curve.P_256)
                .keyUse(KeyUse.SIGNATURE)
                .keyID(UUID.randomUUID().toString())
                .algorithm(JWSAlgorithm.ES256)
                .issueTime(new Date())
                .generate();

    }
}
