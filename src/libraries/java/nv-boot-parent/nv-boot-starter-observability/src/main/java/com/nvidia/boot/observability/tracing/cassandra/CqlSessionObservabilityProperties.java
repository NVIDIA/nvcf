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

package com.nvidia.boot.observability.tracing.cassandra;

import io.micrometer.observation.ObservationRegistry;
import lombok.Data;
import org.springframework.data.cassandra.observability.ObservableCqlSessionFactory;

/**
 * Gates the fallback {@link ObservableCqlSessionFactory#wrap} applied by
 * {@code CassandraTracingAutoConfiguration#observableCqlSession} when
 * {@code nv-boot-starter-cassandra}'s {@code cassandraSession} bean is absent.
 * <p>
 * By default, library wraps whenever an {@link ObservationRegistry} is available. Apps
 * that want to instrument observability manually (e.g. {@code ObservableReactiveSessionFactoryBean})
 * should opt out to avoid duplicate + orphan spans.
 * <p>
 * Note: not used when {@code nv-boot-starter-cassandra} is present. Register a bean of {@code nv-boot-starter-cassandra}'s RefreshingCqlSessionObservabilityProperties instead
 * <pre>
 * {@code
 * @Configuration
 * class CqlSessionObservabilityConfig {
 *
 *     @Bean
 *     CqlSessionObservabilityProperties cqlSessionObservabilityProperties() {
 *         var props = new CqlSessionObservabilityProperties();
 *         props.setEnabled(false);
 *         return props;
 *     }
 * }
 * </pre>
 */
@Data
public class CqlSessionObservabilityProperties {

    private boolean enabled = true;
}
