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
package com.nvidia.nvcf.rest.function.management.dto;

import static org.assertj.core.api.Assertions.assertThat;

import jakarta.validation.ConstraintViolation;
import jakarta.validation.Validation;
import jakarta.validation.Validator;
import jakarta.validation.ValidatorFactory;
import java.net.URI;
import java.util.Set;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class CreateFunctionRequestHelmChartServiceNameTest {

    // 63 characters, the DNS label limit.
    private static final String MAX_LENGTH_NAME =
            "a0123456789" + "0123456789" + "0123456789" + "0123456789" + "0123456789"
                    + "0123456789" + "bc";

    private ValidatorFactory factory;
    private Validator validator;

    @BeforeAll
    void setUp() {
        factory = Validation.buildDefaultValidatorFactory();
        validator = factory.getValidator();
    }

    @AfterAll
    void tearDown() {
        factory.close();
    }

    @ParameterizedTest
    @ValueSource(strings = {"dynamo-frontend", "ENTRYPOINT", "a", "svc1", "1svc",
            MAX_LENGTH_NAME, ""})
    void dnsLabelIsAccepted(String serviceName) {
        assertThat(serviceNameViolations(serviceName)).isEmpty();
    }

    @ParameterizedTest
    @ValueSource(strings = {"dynamo-frontend\nEXTRA=1", "svc\r", "svc.namespace", "svc_name",
            "-svc", "svc-", "svc name", MAX_LENGTH_NAME + "a"})
    void nonDnsLabelIsRejected(String serviceName) {
        assertThat(serviceNameViolations(serviceName))
                .isNotEmpty()
                .allMatch(v -> v.getMessage().startsWith("Invalid helmChartServiceName"));
    }

    private Set<ConstraintViolation<CreateFunctionRequest>> serviceNameViolations(
            String serviceName) {
        var request = CreateFunctionRequest.builder()
                .name("f")
                .inferenceUrl(URI.create("/v1/chat/completions"))
                .helmChart(URI.create("https://helm.example.com/chart-1.0.0.tgz"))
                .helmChartServiceName(serviceName)
                .build();
        return validator.validateProperty(request, "helmChartServiceName");
    }
}
