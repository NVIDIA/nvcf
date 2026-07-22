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

package com.nvidia.boot.telemetry.client;

import static org.assertj.core.api.Assertions.assertThat;

import jakarta.validation.Validation;
import jakarta.validation.Validator;
import jakarta.validation.ValidatorFactory;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;

/**
 * Bean validation on {@link TelemetryProperties}.
 */
class TelemetryPropertiesValidationTest {

    private static ValidatorFactory validatorFactory;
    private static Validator validator;

    @BeforeAll
    static void setUpValidator() {
        validatorFactory = Validation.buildDefaultValidatorFactory();
        validator = validatorFactory.getValidator();
    }

    @AfterAll
    static void tearDown() {
        if (validatorFactory != null) {
            validatorFactory.close();
        }
    }

    @Test
    void validPropertiesPassValidation() {
        var props = TelemetryTestFixtures.telemetryProperties("https://example.com", "/token");
        assertThat(validator.validate(props)).isEmpty();
    }

    @Test
    void blankUrlFailsValidation() {
        var oauth2 = TelemetryProperties.OAuth2Properties.builder()
                .tokenUri("https://example.com/token")
                .clientId("id")
                .clientSecret("secret")
                .build();
        var props = TelemetryProperties.builder()
                .url(" ")
                .pathPrefix("/api/v2/topic")
                .source("src")
                .oauth2(oauth2)
                .build();

        var violations = validator.validate(props);
        assertThat(violations).isNotEmpty();
        assertThat(violations).anyMatch(v -> v.getPropertyPath().toString().equals("url"));
    }

    @Test
    void blankPathPrefixFailsValidation() {
        var oauth2 = TelemetryProperties.OAuth2Properties.builder()
                .tokenUri("https://example.com/token")
                .clientId("id")
                .clientSecret("secret")
                .build();
        var props = TelemetryProperties.builder()
                .url("https://example.com")
                .pathPrefix("")
                .source("src")
                .oauth2(oauth2)
                .build();

        assertThat(validator.validate(props)).anyMatch(
                v -> v.getPropertyPath().toString().equals("pathPrefix"));
    }

    @Test
    void defaultAuthMethodIsClientSecretPost() {
        var props = TelemetryTestFixtures.telemetryProperties("https://example.com", "/token");
        assertThat(props.getOauth2().getAuthMethod())
                .isEqualTo(OAuth2AuthMethod.CLIENT_SECRET_POST);
    }

    @Test
    void oauth2MissingClientSecretFailsValidation() {
        var oauth2 = TelemetryProperties.OAuth2Properties.builder()
                .tokenUri("https://example.com/token")
                .clientId("id")
                .clientSecret("")
                .build();
        var props = TelemetryProperties.builder()
                .url("https://example.com")
                .pathPrefix("/p")
                .source("src")
                .oauth2(oauth2)
                .build();

        assertThat(validator.validate(props)).anyMatch(
                v -> v.getPropertyPath().toString().equals("oauth2.clientSecret"));
    }
}
