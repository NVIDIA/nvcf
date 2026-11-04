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
package com.nvidia.ess.config;

import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.observation.Observation;
import io.micrometer.observation.ObservationRegistry;
import java.util.Set;
import org.aspectj.lang.ProceedingJoinPoint;
import org.aspectj.lang.annotation.Around;
import org.aspectj.lang.annotation.Aspect;
import org.aspectj.lang.reflect.MethodSignature;
import org.reactivestreams.Publisher;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.stereotype.Repository;
import reactor.core.observability.micrometer.Micrometer;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

/**
 * Trace spans for {@link Repository @Repository} types. Reactive return types
 * use
 * {@link Micrometer#observation Micrometer.observation()} for proper context
 * propagation.
 * <p>
 * Removable once Spring Boot supports repository observations natively in 4.x
 * (<a href=
 * "https://github.com/spring-projects/spring-boot/issues/47969">spring-boot#47969</a>).
 * <p>
 * Ref: <a href=
 * "https://projectreactor.io/docs/core/release/reference/metrics.html">Reactor
 * micrometer instrumentation</a>
 * 
 * <p>Do not put {@code @Observed} on {@link Repository @Repository} interfaces, spans will end up being recorded twice
 */
@Configuration
public class RepositoryObservationAspectConfiguration {

    // a unique name to avoid clashing in the future. Not actually used since metric
    // is filtered out
    static final String OBSERVATION_NAME = "nvidia.repository";

    @Bean
    RepositoryObservationAspect repositoryObservationAspect(ObservationRegistry registry) {
        return new RepositoryObservationAspect(registry);
    }

    // Filter out metric, instrumentation added for tracing, not metrics. Spring
    // data metrics already provided with sufficient tags
    @Bean
    MeterFilter suppressRepositoryObservationMetrics() {
        Set<String> suppressed = Set.of(OBSERVATION_NAME, OBSERVATION_NAME + ".active");
        return MeterFilter.deny(id -> suppressed.contains(id.getName()));
    }

    @Aspect
    static class RepositoryObservationAspect {

        private static final Set<String> SKIP_METHODS = Set.of(
                "hashCode", "toString", "equals", "getClass", "notify", "notifyAll", "wait");

        private final ObservationRegistry registry;

        RepositoryObservationAspect(ObservationRegistry registry) {
            this.registry = registry;
        }

        @SuppressWarnings("java:S1612") // ProceedingJoinPoint::proceed has ambiguous method overloads
        @Around("@within(org.springframework.stereotype.Repository)")
        public Object observe(ProceedingJoinPoint pjp) throws Throwable {
            String methodName = pjp.getSignature().getName();
            if (SKIP_METHODS.contains(methodName)) {
                return pjp.proceed();
            }

            RepoInfo repo = resolveRepositoryInfo(pjp);
            String contextualName = repo.simpleName + "." + methodName;

            Class<?> returnType = ((MethodSignature) pjp.getSignature()).getMethod().getReturnType();
            if (Publisher.class.isAssignableFrom(returnType)) {
                Object result = pjp.proceed();
                if (result instanceof Mono<?> mono) {
                    return mono.tap(Micrometer.observation(registry,
                            r -> newObservation(r, contextualName, repo.fqcn, methodName)));
                } else if (result instanceof Flux<?> flux) {
                    return flux.tap(Micrometer.observation(registry,
                            r -> newObservation(r, contextualName, repo.fqcn, methodName)));
                }
                return result;
            }

            // should not be present likely, but just in case some code path ends up using a non reactive JPA repository
            Observation observation = newObservation(registry, contextualName, repo.fqcn, methodName);
            return observation.observeChecked(() -> pjp.proceed());
        }

        private static Observation newObservation(ObservationRegistry registry,
                String contextualName, String codeNamespace, String codeFunction) {
            return Observation.createNotStarted(OBSERVATION_NAME, registry)
                    .contextualName(contextualName)
                    .highCardinalityKeyValue("code.function", codeFunction)
                    .highCardinalityKeyValue("code.namespace", codeNamespace);
        }

        private record RepoInfo(String simpleName, String fqcn) {
        }

        // deal with both JPA interfaces and concrete classes implementing a JPA
        // interface with @Repository
        private static RepoInfo resolveRepositoryInfo(ProceedingJoinPoint pjp) {
            Class<?> targetClass = pjp.getTarget().getClass();

            if (targetClass.isAnnotationPresent(Repository.class)) {
                return new RepoInfo(targetClass.getSimpleName(), targetClass.getName());
            }

            for (Class<?> iface : targetClass.getInterfaces()) {
                if (iface.isAnnotationPresent(Repository.class)) {
                    return new RepoInfo(iface.getSimpleName(), iface.getName());
                }
            }

            return new RepoInfo(targetClass.getSimpleName(), targetClass.getName());
        }
    }

}
