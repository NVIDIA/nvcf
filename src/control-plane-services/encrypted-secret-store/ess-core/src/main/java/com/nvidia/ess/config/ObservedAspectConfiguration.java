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

import io.micrometer.common.KeyValues;
import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.observation.Observation;
import io.micrometer.observation.ObservationRegistry;
import io.micrometer.observation.annotation.Observed;
import io.micrometer.observation.aop.ObservedAspect;
import java.util.Set;
import java.util.concurrent.CompletionStage;
import org.aspectj.lang.ProceedingJoinPoint;
import org.aspectj.lang.annotation.Around;
import org.aspectj.lang.annotation.Aspect;
import org.aspectj.lang.reflect.MethodSignature;
import org.jspecify.annotations.Nullable;
import org.reactivestreams.Publisher;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import reactor.core.observability.micrometer.Micrometer;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

/**
 * Unified {@link Observed @Observed} aspect that handles sync, {@link CompletionStage},
 * and reactive ({@link Mono}/{@link Flux}) return types.
 *
 * <p>Replaces default
 * {@link ObservedAspect}. Default is <b>not</b> registered because:
 * <ul>
 *   <li>It has no {@code Mono}/{@code Flux} support</li>
 *   <li>Its contextual-name delimiter ({@code #}) is hardcoded in the package-private
 *       {@code ObservedAspectObservationDocumentation}</li>
 * </ul>
 *
 * <p>{@code management.observations.annotations.enable} auto-configures the whole stack, but works with sync code only
 *
 * <p>Observations whose name equals {@link #TRACE_ONLY_NAME} produce only trace spans;
 * their meters are suppressed via a {@link MeterFilter}.
 */
@Configuration
public class ObservedAspectConfiguration {

    /**
     * Observation name for tracing-only {@code @Observed} methods (meters denied).
     * <p>
     * Point to ess-encryption constant to avoid depending on library internals too much in this repo
     */
    public static final String TRACE_ONLY_NAME = com.nvidia.ess.encryption.constants.Constants.TRACE_ONLY_NAME;

    @Bean
    ObservedMethodAspect observedMethodAspect(ObservationRegistry registry) {
        return new ObservedMethodAspect(registry);
    }

    @Bean
    MeterFilter suppressTraceOnlyObservationMetrics() {
        Set<String> suppressed = Set.of(TRACE_ONLY_NAME, TRACE_ONLY_NAME + ".active");
        return MeterFilter.deny(id -> suppressed.contains(id.getName()));
    }

    @Aspect
    static class ObservedMethodAspect {

        private final ObservationRegistry registry;

        ObservedMethodAspect(ObservationRegistry registry) {
            this.registry = registry;
        }

        @SuppressWarnings("java:S1612") // ProceedingJoinPoint::proceed has ambiguous method overloads
        @Around("execution (@io.micrometer.observation.annotation.Observed * *.*(..))")
        public Object observe(ProceedingJoinPoint pjp) throws Throwable {
            MethodSignature sig = (MethodSignature) pjp.getSignature();
            Observed observed = sig.getMethod().getAnnotation(Observed.class);
            Class<?> returnType = sig.getMethod().getReturnType();

            String name = observed.name().isEmpty() ? "method.observed" : observed.name();
            String contextualName = observed.contextualName().isEmpty()
                    ? sig.getDeclaringType().getSimpleName() + "." + sig.getName()
                    : observed.contextualName();
            String className = sig.getDeclaringTypeName();
            String methodName = sig.getName();
            KeyValues extraKvs = KeyValues.of(observed.lowCardinalityKeyValues());

            if (Publisher.class.isAssignableFrom(returnType)) {
                // TODO does not handle exceptions thrown before returning the reactive publisher type (NPE, etc.)
                //  propagating spans correctly likely requires connecting the observation through the Reactive context
                //  which will end up changing behavior of annotated methods
                Object result = pjp.proceed();
                if (result instanceof Mono<?> mono) {
                    return mono.tap(Micrometer.observation(registry,
                            r -> newObservation(r, pjp, name, contextualName, className,
                                    methodName, extraKvs)));
                } else if (result instanceof Flux<?> flux) {
                    return flux.tap(Micrometer.observation(registry,
                            r -> newObservation(r, pjp, name, contextualName, className,
                                    methodName, extraKvs)));
                }
                return result;
            }

            Observation observation = newObservation(registry, pjp, name, contextualName, className, methodName, extraKvs);

            // copy from ObservedAspect
            if (CompletionStage.class.isAssignableFrom(sig.getReturnType())) {
                observation.start();
                Observation.Scope scope = observation.openScope();
                try {
                    Object result = pjp.proceed();
                    if (result == null) {
                        stopObservation(observation, null);
                        return result;
                    }
                    else {
                        CompletionStage<?> stage = (CompletionStage<?>) result;
                        return stage.whenComplete((res, error) -> stopObservation(observation, error));
                    }
                }
                catch (Throwable error) {
                    stopObservation(observation, error);
                    throw error;
                }
                finally {
                    scope.close();
                }
            }

            return observation
                    .observeChecked(() -> pjp.proceed());
        }

        private void stopObservation(Observation observation, @Nullable Throwable error) {
            if (error != null) {
                observation.error(error);
            }
            observation.stop();
        }

        private Observation newObservation(ObservationRegistry reg, ProceedingJoinPoint pjp,
                String name, String contextualName, String codeNamespace,
                String codeFunction, KeyValues extraKvs) {
            return Observation.createNotStarted(name,
                            () -> new ObservedAspect.ObservedAspectContext(pjp), reg)
                    .contextualName(contextualName)
                    .highCardinalityKeyValue("code.namespace", codeNamespace)
                    .highCardinalityKeyValue("code.function", codeFunction)
                    .lowCardinalityKeyValues(extraKvs);
        }
    }
}
