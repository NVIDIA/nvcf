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
package com.nvidia.nvcf;

import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import io.grpc.health.v1.HealthCheckRequest;
import io.grpc.health.v1.HealthCheckResponse;
import io.grpc.health.v1.HealthCheckResponse.ServingStatus;
import io.grpc.health.v1.HealthGrpc;
import io.grpc.protobuf.services.HealthStatusManager;
import io.grpc.stub.StreamObserver;
import java.io.IOException;
import java.io.UncheckedIOException;
import org.junit.jupiter.api.Test;
import org.springframework.boot.autoconfigure.AutoConfigurations;
import org.springframework.boot.env.YamlPropertySourceLoader;
import org.springframework.boot.grpc.server.autoconfigure.health.GrpcServerHealthAutoConfiguration;
import org.springframework.boot.grpc.server.health.GrpcServerHealth;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.boot.health.registry.DefaultHealthContributorRegistry;
import org.springframework.boot.health.registry.HealthContributorRegistry;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.core.io.ClassPathResource;

class GrpcHealthConfigurationTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withConfiguration(AutoConfigurations.of(GrpcServerHealthAutoConfiguration.class))
            .withInitializer(context -> {
                try {
                    var sources = new YamlPropertySourceLoader()
                            .load("application", new ClassPathResource("application.yaml"));
                    sources.forEach(source -> context.getEnvironment().getPropertySources().addLast(source));
                } catch (IOException e) {
                    throw new UncheckedIOException(e);
                }
            })
            .withPropertyValues("spring.grpc.server.health.enabled=true")
            .withBean(HealthContributorRegistry.class, () -> {
                var registry = new DefaultHealthContributorRegistry();
                registry.registerContributor("unavailableDependency", (HealthIndicator) () -> Health.down().build());
                return registry;
            });

    @Test
    void grpcHealthRemainsServingWhenAnActuatorContributorIsDown() {
        assertHealth(contextRunner, ServingStatus.SERVING);
    }

    @Test
    void overallHealthCanBeExplicitlyEnabled() {
        assertHealth(contextRunner.withPropertyValues("spring.grpc.server.health.include-overall-health=true"),
                     ServingStatus.NOT_SERVING);
    }

    @SuppressWarnings("unchecked")
    private static void assertHealth(ApplicationContextRunner runner, ServingStatus expected) {
        runner.run(context -> {
            var manager = context.getBean(HealthStatusManager.class);
            context.getBean(GrpcServerHealth.class).update(manager);
            var observer = (StreamObserver<HealthCheckResponse>) mock(StreamObserver.class);
            ((HealthGrpc.HealthImplBase) manager.getHealthService())
                    .check(HealthCheckRequest.getDefaultInstance(), observer);
            verify(observer).onNext(HealthCheckResponse.newBuilder().setStatus(expected).build());
            verify(observer).onCompleted();
            verifyNoMoreInteractions(observer);
        });
    }
}
