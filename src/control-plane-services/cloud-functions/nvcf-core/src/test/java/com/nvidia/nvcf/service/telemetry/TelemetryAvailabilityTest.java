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
package com.nvidia.nvcf.service.telemetry;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatCode;
import static org.assertj.core.api.Assertions.catchThrowableOfType;

import com.nvidia.boot.exceptions.BadRequestException;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.core.env.SystemEnvironmentPropertySource;
import org.springframework.http.HttpStatus;

class TelemetryAvailabilityTest {

    private final ApplicationContextRunner runner = new ApplicationContextRunner()
            .withUserConfiguration(TelemetryAvailability.class);

    @Test
    void enabledWhenUnset() {
        runner.run(context -> assertThatCode(
                context.getBean(TelemetryAvailability.class)::requireEnabled)
                .doesNotThrowAnyException());
    }

    @Test
    void explicitlyEnabled() {
        runner.withPropertyValues("nvcf.telemetry.enabled=true")
                .run(context -> assertThatCode(
                        context.getBean(TelemetryAvailability.class)::requireEnabled)
                        .doesNotThrowAnyException());
    }

    @Test
    void disabledReturnsBadRequestWithDeploymentMessage() {
        runner.withPropertyValues("nvcf.telemetry.enabled=false").run(context -> {
            var error = catchThrowableOfType(BadRequestException.class,
                    context.getBean(TelemetryAvailability.class)::requireEnabled);
            assertThat(error).isNotNull();
            assertThat(error.getStatusCode()).isEqualTo(HttpStatus.BAD_REQUEST);
            assertThat(error.getBody().getDetail())
                    .isEqualTo("telemetry is not supported on this deployment");
        });
    }

    @Test
    void disabledThroughEnvironmentVariable() {
        runner.withInitializer(context -> context.getEnvironment().getPropertySources()
                .addFirst(new SystemEnvironmentPropertySource("telemetry-env",
                        Map.of("NVCF_TELEMETRY_ENABLED", "false"))))
                .run(context -> assertThat(catchThrowableOfType(BadRequestException.class,
                        context.getBean(TelemetryAvailability.class)::requireEnabled))
                        .isNotNull());
    }
}
