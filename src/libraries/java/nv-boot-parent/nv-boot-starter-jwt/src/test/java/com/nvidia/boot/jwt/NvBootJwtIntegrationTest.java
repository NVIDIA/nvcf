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

package com.nvidia.boot.jwt;

import static org.assertj.core.api.Assertions.assertThat;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.jwt.configuration.EncryptedModelConverterProperties;
import com.nvidia.boot.jwt.configuration.JweKeysMapping;
import com.nvidia.boot.jwt.configuration.PrivateJwksString;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import com.nvidia.boot.jwt.services.mapping.model.ServiceModel;
import com.nvidia.boot.jwt.services.mapping.vo.ServiceVo;
import java.io.IOException;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.autoconfigure.SpringBootApplication;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Import;
import org.springframework.core.io.ClassPathResource;

/**
 * Integration test verifying nv-boot-starter-jwt auto-configuration and beans work end-to-end.
 * Uses the same configuration pattern as a real application: app provides PrivateJwksString
 * and JweKeysMapping, library auto-configures JwtService, EncryptedModelConverter, etc.
 */
@SpringBootTest(
        classes = NvBootJwtIntegrationTest.App.class,
        webEnvironment = WebEnvironment.NONE)
class NvBootJwtIntegrationTest {

    private static final String TEST_KEY = "test-A256GCM-key";

    @SpringBootApplication
    @Import(App.JwtKeysConfiguration.class)
    static class App {

        @Configuration
        static class JwtKeysConfiguration {

            private static final JweKeysMapping jweKeysMapping = JweKeysMapping
                    .builder()
                    .keysMapping(Map.of(TEST_KEY, "test-A256GCM-key"))
                    .build();

            @Bean
            public PrivateJwksString privateJwks()
                    throws IOException {
                var keyResource = new ClassPathResource("keys/dev_jwks_private.json");
                return new PrivateJwksString(
                        new String(keyResource.getInputStream().readAllBytes()));
            }

            @Bean
            public JweKeysMapping jweKeysMapping() {
                return jweKeysMapping;
            }

            @Bean
            public EncryptedModelConverterProperties encryptedModelConverterProperties() {
                var props = new EncryptedModelConverterProperties();
                props.setBasePackage("com.nvidia.boot.jwt.services.mapping");
                return props;
            }
        }
    }

    @Autowired
    private JwtService jwtService;

    @Autowired
    private EncryptedModelConverter<ServiceModel, ServiceVo> serviceEncryptedModelConverter;

    @Test
    void jwtServiceSignAndValidate() throws Exception {
        var jti = UUID.randomUUID().toString();
        var claimsSet = new JWTClaimsSet.Builder()
                .jwtID(jti)
                .build();

        var signedJWT = new SignedJWT(
                new JWSHeader.Builder(JWSAlgorithm.ES256)
                        .keyID("dev-key-id")
                        .build(),
                claimsSet);

        jwtService.signJwt(signedJWT, "dev-key-id");
        var serializedJwt = signedJWT.serialize();

        // getJwtClaimsSet validates signature internally and returns claims when valid
        var parsedClaims = jwtService.getJwtClaimsSet(serializedJwt);
        assertThat(parsedClaims.getClaim("jti")).isEqualTo(jti);
    }

    @Test
    void jwtServiceEncryptAndDecrypt() {
        var payload = "secret message";
        var encrypted = jwtService.encryptWithKeysetName(TEST_KEY, payload);
        var decrypted = jwtService.decrypt(encrypted);
        assertThat(decrypted).isEqualTo(payload);
    }

    @Test
    void encryptedModelConverterRoundTrip() {
        var vo = ServiceVo.builder()
                .serviceId("service-id-1")
                .sfClientIds(Set.of("client-1", "client-2"))
                .maxApiKeysPerUser(2)
                .maxApiKeyTtlDays(3)
                .maxAuthzSizeChars(4)
                .minAuthzUpdateIntervalSeconds(5)
                .maxApiKeysPerAccount(6)
                .build();

        var model = serviceEncryptedModelConverter.voToModel(vo);
        var roundTripped = serviceEncryptedModelConverter.modelToVo(model);

        assertThat(roundTripped).isEqualTo(vo);
    }
}
