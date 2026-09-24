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
package com.nvidia.nvcf.configuration.scheduler;

import static com.nvidia.nvcf.util.NvcfConstants.MAX_THREAD_POOL_SIZE;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.util.List;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.autoconfigure.context.ConfigurationPropertiesAutoConfiguration;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.context.properties.source.MapConfigurationPropertySource;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;

class FunctionDeploymentsTaskPropertiesTest {

    private static final String PREFIX = "nvcf.scheduler.function-deployments";
    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withConfiguration(AutoConfigurations.of(
                    ConfigurationPropertiesAutoConfiguration.class,
                    RefreshAutoConfiguration.class))
            .withUserConfiguration(FunctionDeploymentsTaskProperties.class);

    @Test
    void shouldBindRegionsAndMaxConcurrency() {
        var source = new MapConfigurationPropertySource(Map.of(
                PREFIX + ".current-region", "us-west-2",
                PREFIX + ".regions[0]", "us-east-1",
                PREFIX + ".regions[1]", "us-west-2",
                PREFIX + ".regions[2]", "eu-west-1",
                PREFIX + ".max-concurrency", "64"));

        var properties = new Binder(source)
                .bind(PREFIX, Bindable.of(FunctionDeploymentsTaskProperties.class))
                .orElseThrow(IllegalStateException::new);
        properties.validateAndNormalize();

        assertThat(properties.getCurrentRegion()).isEqualTo("us-west-2");
        assertThat(properties.getRegions())
                .containsExactly("us-east-1", "us-west-2", "eu-west-1");
        assertThat(properties.getMaxConcurrency()).isEqualTo(64);
    }

    @Test
    void shouldUseCurrentRegionWhenRegionsAreNotConfigured() {
        var properties = new FunctionDeploymentsTaskProperties();
        properties.setCurrentRegion("us-west-2");

        properties.validateAndNormalize();

        assertThat(properties.getRegions()).containsExactly("us-west-2");
    }

    @Test
    void shouldUseDefaultMaxConcurrencyWhenNotConfigured() {
        var properties = new FunctionDeploymentsTaskProperties();
        properties.setCurrentRegion("us-west-2");

        properties.validateAndNormalize();

        assertThat(properties.getMaxConcurrency()).isEqualTo(Math.min(
                MAX_THREAD_POOL_SIZE, Runtime.getRuntime().availableProcessors()));
    }

    @Test
    void shouldRejectCurrentRegionOutsideConfiguredRegions() {
        var properties = new FunctionDeploymentsTaskProperties();
        properties.setCurrentRegion("ap-south-1");
        properties.setRegions(List.of("us-east-1", "us-west-2", "eu-west-1"));

        assertThatThrownBy(properties::validateAndNormalize)
                .isInstanceOf(IllegalStateException.class)
                .hasMessage(
                        "Current region 'ap-south-1' must be included in the list of "
                                + "configured regions '[us-east-1, us-west-2, eu-west-1]'");
    }

    @Test
    void shouldRejectDuplicateRegions() {
        var properties = new FunctionDeploymentsTaskProperties();
        properties.setCurrentRegion("us-east-1");
        properties.setRegions(List.of("us-east-1", "us-west-2", "us-east-1"));

        assertThatThrownBy(properties::validateAndNormalize)
                .isInstanceOf(IllegalStateException.class)
                .hasMessage(
                        "Configured function deployment regions must not contain duplicates: "
                                + "[us-east-1, us-west-2, us-east-1]");
    }

    @Test
    void shouldRejectMissingCurrentRegion() {
        var properties = new FunctionDeploymentsTaskProperties();

        assertThatThrownBy(properties::validateAndNormalize)
                .isInstanceOf(IllegalStateException.class);
    }

    @Test
    void shouldRejectWhitespacePaddedCurrentRegion() {
        var properties = new FunctionDeploymentsTaskProperties();
        properties.setCurrentRegion(" us-west-2 ");

        assertThatThrownBy(properties::validateAndNormalize)
                .isInstanceOf(IllegalStateException.class)
                .hasMessage(
                        "nvcf.scheduler.function-deployments.current-region must not contain " +
                                "surrounding whitespace");
    }

    @Test
    void shouldRejectBlankOrWhitespacePaddedConfiguredRegions() {
        for (var invalidRegion : List.of("", "   ", " us-west-2", "us-west-2 ")) {
            var properties = new FunctionDeploymentsTaskProperties();
            properties.setCurrentRegion("us-east-1");
            properties.setRegions(List.of("us-east-1", invalidRegion));

            assertThatThrownBy(properties::validateAndNormalize)
                    .isInstanceOf(IllegalStateException.class)
                    .hasMessageContaining(
                            "Configured function deployment regions must contain only nonblank, " +
                                    "trimmed values");
        }
    }

    @Test
    void shouldRejectInvalidMaxConcurrency() {
        for (var maxConcurrency : List.of(0, -1)) {
            var properties = new FunctionDeploymentsTaskProperties();
            properties.setCurrentRegion("us-west-2");
            properties.setMaxConcurrency(maxConcurrency);

            assertThatThrownBy(properties::validateAndNormalize)
                    .isInstanceOf(IllegalStateException.class)
                    .hasMessage(
                            "nvcf.scheduler.function-deployments.max-concurrency must be at least 1");
        }
    }

    @Test
    void shouldNotCreatePropertiesWhenSchedulerIsDisabled() {
        contextRunner.withPropertyValues("nvcf.scheduler.enabled=false")
                .run(context -> assertThat(context)
                        .doesNotHaveBean("functionDeploymentsTaskProperties"));
    }

    @Test
    void shouldCreateNamedPropertiesBeanWhenSchedulerIsEnabled() {
        contextRunner.withPropertyValues(
                             "nvcf.scheduler.enabled=true",
                             PREFIX + ".current-region=us-west-2",
                             PREFIX + ".regions[0]=us-east-1",
                             PREFIX + ".regions[1]=us-west-2")
                .run(context -> {
                    assertThat(context).hasBean("functionDeploymentsTaskProperties");
                    var properties = context.getBean(
                            "functionDeploymentsTaskProperties",
                            FunctionDeploymentsTaskProperties.class);
                    assertThat(properties.getCurrentRegion()).isEqualTo("us-west-2");
                    assertThat(properties.getRegions())
                            .containsExactly("us-east-1", "us-west-2");
                });
    }
}
