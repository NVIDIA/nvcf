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

import static org.assertj.core.api.Assertions.assertThat;

import com.nimbusds.jose.util.IOUtils;
import com.nvidia.boot.jwt.services.mapping.EncryptedModelConverter;
import java.io.IOException;
import java.util.Map;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.support.AbstractBeanDefinition;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.jackson.autoconfigure.JacksonAutoConfiguration;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.core.io.ClassPathResource;
import tools.jackson.databind.json.JsonMapper;

class JwksConfigurationTest {

    private static final String TEST_JWKS_FILE = "keys/dev_jwks_private.json";

    private final JwksConfiguration jwksConfiguration = new JwksConfiguration();

    private final ApplicationContextRunner runner = new ApplicationContextRunner()
            .withConfiguration(AutoConfigurations.of(JacksonAutoConfiguration.class))
            .withUserConfiguration(JwksConfiguration.class)
            .withBean(PrivateJwksString.class, () -> {
                try {
                    var keyResource = new ClassPathResource(TEST_JWKS_FILE);
                    return new PrivateJwksString(
                            IOUtils.readInputStreamToString(keyResource.getInputStream()));
                } catch (IOException e) {
                    throw new RuntimeException(e);
                }
            })
            .withBean(JweKeysMapping.class, () -> JweKeysMapping.builder()
                    .keysMapping(Map.of("test-A256GCM-key", "test-A256GCM-key"))
                    .build());

    @Test
    void jwkSet()
            throws IOException {
        var jwksString = IOUtils.readInputStreamToString(
                new ClassPathResource(TEST_JWKS_FILE).getInputStream());
        var jwkSet = jwksConfiguration.jwkSet(new PrivateJwksString(jwksString));
        assertThat(jwkSet.getKeyByKeyId("dev-key-id")).isNotNull();
        assertThat(jwkSet.getKeyByKeyId("test-A256GCM-key")).isNotNull();
    }

    @Test
    @DisplayName("EncryptedModelConverter is created when EncryptedModelConverterProperties bean is present")
    void encryptedModelConverterCreatedWhenPropertiesPresent() {
        runner.withBean(EncryptedModelConverterProperties.class, () -> {
            var props = new EncryptedModelConverterProperties();
            props.setBasePackage("com.nvidia.boot");
            return props;
        }).run(context -> assertThat(context).hasBean("encryptedModelConverter"));
    }

    @Test
    @DisplayName("EncryptedModelConverter is not created when EncryptedModelConverterProperties bean is absent")
    void encryptedModelConverterNotCreatedWhenPropertiesAbsent() {
        runner.run(context -> assertThat(context).doesNotHaveBean(EncryptedModelConverter.class));
    }

    @Test
    @DisplayName("jwkSet bean is @RefreshScope so vault rotation rebuilds the keyset")
    void jwkSetIsRefreshScoped() {
        runner.run(context -> {
            var beanFactory = context.getSourceApplicationContext().getBeanFactory();
            assertThat(beanFactory.getBeanDefinition("scopedTarget.jwkSet").getScope())
                    .isEqualTo("refresh");
        });
    }

    @Test
    @DisplayName("jwtService bean is @RefreshScope so vault rotation rebuilds the signer")
    void jwtServiceIsRefreshScoped() {
        runner.run(context -> {
            var beanFactory = context.getSourceApplicationContext().getBeanFactory();
            assertThat(beanFactory.getBeanDefinition("scopedTarget.jwtService").getScope())
                    .isEqualTo("refresh");
        });
    }

    @Test
    @DisplayName("genericEncryptedJsonMapper is not a default autowire candidate — "
            + "apps keep Spring Boot's primary JsonMapper for HTTP message conversion")
    void genericEncryptedJsonMapperIsNotDefaultCandidate() {
        runner.run(context -> {
            var beanFactory = context.getSourceApplicationContext().getBeanFactory();
            assertThat(beanFactory.containsBean("genericEncryptedJsonMapper")).isTrue();
            var bd = (AbstractBeanDefinition)
                    beanFactory.getBeanDefinition("genericEncryptedJsonMapper");
            assertThat(bd.isDefaultCandidate()).isFalse();
            assertThat(context.getBean(JsonMapper.class))
                    .isNotSameAs(context.getBean("genericEncryptedJsonMapper", JsonMapper.class));
        });
    }
}
