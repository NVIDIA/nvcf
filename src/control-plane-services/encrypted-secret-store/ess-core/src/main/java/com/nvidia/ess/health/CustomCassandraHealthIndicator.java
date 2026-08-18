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
package com.nvidia.ess.health;

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.core.cql.SimpleStatement;
import com.nvidia.ess.filter.ReactiveRequestContextHolder;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.health.contributor.AbstractReactiveHealthIndicator;
import org.springframework.boot.health.contributor.Health;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;
import org.springframework.stereotype.Component;
import reactor.core.publisher.Mono;

@Component("customcassandra")
@Slf4j
public class CustomCassandraHealthIndicator extends AbstractReactiveHealthIndicator {

    private final ReactiveCassandraTemplate reactiveCassandraTemplate;

    private final TelemetryComponents telemetryComponents;

    public CustomCassandraHealthIndicator(ReactiveCassandraTemplate reactiveCassandraTemplate,
            @Qualifier(TelemetryComponentsImpl.BEAN_NAME) TelemetryComponents telemetryComponents) {
        this.reactiveCassandraTemplate = reactiveCassandraTemplate;
        this.telemetryComponents = telemetryComponents;
    }

    @Override
    protected Mono<Health> doHealthCheck(Health.Builder builder) {
        return ReactiveRequestContextHolder.getExchange().flatMap(exchange -> {
            SimpleStatement statement = SimpleStatement.builder(
                            "SELECT toTimestamp(now()) AS current_time FROM system.local")
                    .setConsistencyLevel(ConsistencyLevel.LOCAL_QUORUM)
                    .build();
            return reactiveCassandraTemplate
                    .selectOne(statement, Map.class)
                    .doOnSuccess(map -> log.debug("Got timeuuid from C* {}", map))
                    .map(ignored -> builder.up().build())
                    .onErrorResume(e -> {
                        log.error("failed readiness: ", e);
                        telemetryComponents.recordExceptionWithoutErrorStatus(exchange, e);
                        return Mono.just(builder.down(e).build());
                    });
        });
    }
}