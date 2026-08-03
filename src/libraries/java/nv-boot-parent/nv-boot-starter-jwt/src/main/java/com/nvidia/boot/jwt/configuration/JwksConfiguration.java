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

package com.nvidia.boot.jwt.configuration;

import tools.jackson.databind.DeserializationFeature;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.module.blackbird.BlackbirdModule;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.JWKSet;
import com.nvidia.boot.jwt.services.JwtService;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.text.ParseException;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.EnableMBeanExport;
import org.springframework.jmx.support.RegistrationPolicy;

@Configuration
/**
 * @EnableMBeanExport is used to fix JMX registration conflict which only applicable when running
 * locally. JMX must be disabled in all environments as there is no plan to use it and incorrect
 * configuration is a major security threat.
 * <p>
 * application.yaml:
 * spring:
 *   jmx:
 *     enabled: false
 */
@EnableMBeanExport(registration = RegistrationPolicy.IGNORE_EXISTING)
@ConditionalOnBean({PrivateJwksString.class, JweKeysMapping.class})
public class JwksConfiguration {

    @Bean
    @RefreshScope
    public JWKSet jwkSet(PrivateJwksString privateJwksString) {
        try {
            return JWKSet.load(new ByteArrayInputStream(privateJwksString.getValue().getBytes()));
        } catch (ParseException | IOException e) {
            throw new IllegalStateException("Failed to read private JWKs", e);
        }
    }

    @Bean
    @RefreshScope
    public JwtService jwtService(
            JWKSet jwkSet,
            JweKeysMapping jweKeysMapping) throws JOSEException {
        return new JwtService(jwkSet, jweKeysMapping);
    }

    // defaultCandidate = false keeps the bean available by @Qualifier("genericEncryptedJsonMapper")
    // for EncryptedModelConverter, but hides it from type-based resolution so it does not satisfy
    // Spring Boot's @ConditionalOnMissingBean(JsonMapper.class) in JacksonAutoConfiguration. If it
    // did, Spring Boot would skip creating its primary JsonMapper and Spring MVC would fall back
    // to this one for HTTP message conversion — changing serialization semantics (e.g. ProblemDetail
    // .properties is emitted as `null` instead of being omitted).
    @Bean(name = "genericEncryptedJsonMapper", defaultCandidate = false)
    @ConditionalOnMissingBean(name = "genericEncryptedJsonMapper")
    public JsonMapper genericEncryptedJsonMapper() {
        return JsonMapper.builder()
                .addModule(new BlackbirdModule())
                .findAndAddModules()
                .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES) // data migrations
                .build();
    }

    @Bean
    @RefreshScope
    @ConditionalOnBean(EncryptedModelConverterProperties.class)
    public EncryptedModelConverter encryptedModelConverter(
            JwtService jwtService,
            @Qualifier("genericEncryptedJsonMapper") JsonMapper genericEncryptedJsonMapper,
            EncryptedModelConverterProperties encryptedModelConverterProperties) {
        var basePackage = encryptedModelConverterProperties.getBasePackage();
        return new EncryptedModelConverter(jwtService, genericEncryptedJsonMapper, basePackage);
    }
}
