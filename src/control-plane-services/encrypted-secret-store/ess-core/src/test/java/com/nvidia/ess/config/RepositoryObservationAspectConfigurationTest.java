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

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.ess.config.RepositoryObservationAspectConfiguration.RepositoryObservationAspect;
import io.micrometer.core.instrument.Meter.Id;
import io.micrometer.core.instrument.Meter.Type;
import io.micrometer.core.instrument.Tags;
import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.core.instrument.config.MeterFilterReply;
import io.micrometer.observation.ObservationRegistry;
import java.lang.reflect.Method;
import org.aspectj.lang.ProceedingJoinPoint;
import org.aspectj.lang.reflect.MethodSignature;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.stereotype.Repository;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class RepositoryObservationAspectConfigurationTest {

    @Mock
    private ObservationRegistry observationRegistry;

    private final RepositoryObservationAspectConfiguration config =
            new RepositoryObservationAspectConfiguration();

    // --- Bean creation tests ---

    @Test
    void repositoryObservationAspect_shouldReturnAspect() {
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(observationRegistry);

        assertThat(aspect).isNotNull();
    }

    @Test
    void suppressRepositoryObservationMetrics_shouldReturnMeterFilter() {
        MeterFilter filter = config.suppressRepositoryObservationMetrics();

        assertThat(filter).isNotNull();
    }

    // --- MeterFilter tests ---

    @ParameterizedTest
    @ValueSource(strings = {"nvidia.repository", "nvidia.repository.active"})
    void suppressRepositoryObservationMetrics_shouldDenySuppressedMetrics(String metricName) {
        MeterFilter filter = config.suppressRepositoryObservationMetrics();
        Id id = new Id(metricName, Tags.empty(), null, null, Type.COUNTER);

        MeterFilterReply reply = filter.accept(id);

        assertThat(reply).isEqualTo(MeterFilterReply.DENY);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "http.server.requests", "jvm.memory.used", "nvidia.repository.other",
            "cassandra.cql-requests", "nvidia.repositoryx"
    })
    void suppressRepositoryObservationMetrics_shouldAllowOtherMetrics(String metricName) {
        MeterFilter filter = config.suppressRepositoryObservationMetrics();
        Id id = new Id(metricName, Tags.empty(), null, null, Type.COUNTER);

        MeterFilterReply reply = filter.accept(id);

        assertThat(reply).isEqualTo(MeterFilterReply.NEUTRAL);
    }

    // --- Aspect skip-methods tests ---

    @ParameterizedTest
    @ValueSource(strings = {"hashCode", "toString", "equals", "getClass", "notify", "notifyAll", "wait"})
    void observe_shouldSkipObjectMethods(String methodName) throws Throwable {
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(observationRegistry);
        ProceedingJoinPoint pjp = mock(ProceedingJoinPoint.class);
        MethodSignature signature = mock(MethodSignature.class);
        when(pjp.getSignature()).thenReturn(signature);
        when(signature.getName()).thenReturn(methodName);
        Object expected = new Object();
        when(pjp.proceed()).thenReturn(expected);

        Object result = aspect.observe(pjp);

        assertThat(result).isSameAs(expected);
        verify(pjp, never()).getTarget();
    }

    // --- Aspect reactive return type tests ---

    @Test
    void observe_shouldWrapMonoReturnType() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                AnnotatedRepository.class, "findById", Mono.class, Mono.just("value"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create(Mono.class.cast(result))
                .expectNextCount(1)
                .verifyComplete();
    }

    @Test
    void observe_shouldWrapFluxReturnType() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                AnnotatedRepository.class, "findAll", Flux.class, Flux.just("a", "b"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Flux.class);
        StepVerifier.create(Flux.class.cast(result))
                .expectNextCount(2)
                .verifyComplete();
    }

    @Test
    void observe_shouldHandleEmptyMono() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                AnnotatedRepository.class, "findById", Mono.class, Mono.empty());

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create(Mono.class.cast(result))
                .verifyComplete();
    }

    @Test
    void observe_shouldHandleErrorMono() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        RuntimeException error = new RuntimeException("db error");
        ProceedingJoinPoint pjp = mockJoinPoint(
                AnnotatedRepository.class, "findById", Mono.class, Mono.error(error));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create(Mono.class.cast(result))
                .expectErrorSatisfies(e -> assertThat(e).isSameAs(error))
                .verify();
    }

    // --- Aspect non-reactive return type tests ---

    @Test
    void observe_shouldWrapNonReactiveReturnType() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                AnnotatedRepository.class, "count", Long.class, 42L);

        Object result = aspect.observe(pjp);

        assertThat(result).isEqualTo(42L);
    }

    @Test
    void observe_shouldPropagateExceptionForNonReactive() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        RuntimeException error = new RuntimeException("db error");
        ProceedingJoinPoint pjp = mockJoinPointThrowing(
                AnnotatedRepository.class, "count", Long.class, error);

        assertThatThrownBy(() -> aspect.observe(pjp)).isSameAs(error);
    }

    // --- resolveRepositoryInfo tests ---

    @Test
    void observe_shouldUseAnnotatedClassSimpleNameInContextualName() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                AnnotatedRepository.class, "save", Mono.class, Mono.just("ok"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create(Mono.class.cast(result))
                .expectNextCount(1)
                .verifyComplete();
    }

    @Test
    void observe_shouldResolveFromInterfaceWhenTargetClassNotAnnotated() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                ImplWithAnnotatedInterface.class, "findAll", Flux.class, Flux.just("x"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Flux.class);
        StepVerifier.create(Flux.class.cast(result))
                .expectNextCount(1)
                .verifyComplete();
    }

    @Test
    void observe_shouldFallbackToTargetClassWhenNoAnnotation() throws Throwable {
        ObservationRegistry registry = ObservationRegistry.create();
        RepositoryObservationAspect aspect = config.repositoryObservationAspect(registry);
        ProceedingJoinPoint pjp = mockJoinPoint(
                UnannotatedRepo.class, "findAll", Flux.class, Flux.just("y"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Flux.class);
        StepVerifier.create(Flux.class.cast(result))
                .expectNextCount(1)
                .verifyComplete();
    }

    // --- Observation name constant ---

    @Test
    void observationName_shouldMatchExpectedValue() {
        assertThat(RepositoryObservationAspectConfiguration.OBSERVATION_NAME)
                .isEqualTo("nvidia.repository");
    }

    // --- Test helpers ---

    private ProceedingJoinPoint mockJoinPoint(
            Class<?> targetClass, String methodName, Class<?> returnType, Object returnValue)
            throws Throwable {
        ProceedingJoinPoint pjp = mock(ProceedingJoinPoint.class);
        MethodSignature signature = mock(MethodSignature.class);
        when(pjp.getSignature()).thenReturn(signature);
        when(signature.getName()).thenReturn(methodName);
        when(pjp.proceed()).thenReturn(returnValue);
        when(pjp.getTarget()).thenReturn(targetClass.getDeclaredConstructor().newInstance());

        Method method = mockMethodWithReturnType(returnType);
        when(signature.getMethod()).thenReturn(method);

        return pjp;
    }

    private ProceedingJoinPoint mockJoinPointThrowing(
            Class<?> targetClass, String methodName, Class<?> returnType, Throwable error)
            throws Throwable {
        ProceedingJoinPoint pjp = mock(ProceedingJoinPoint.class);
        MethodSignature signature = mock(MethodSignature.class);
        when(pjp.getSignature()).thenReturn(signature);
        when(signature.getName()).thenReturn(methodName);
        when(pjp.proceed()).thenThrow(error);
        when(pjp.getTarget()).thenReturn(targetClass.getDeclaredConstructor().newInstance());

        Method method = mockMethodWithReturnType(returnType);
        when(signature.getMethod()).thenReturn(method);

        return pjp;
    }

    private Method mockMethodWithReturnType(Class<?> returnType) throws NoSuchMethodException {
        if (returnType == Mono.class) return ReturnTypeStubs.class.getDeclaredMethod("monoStub");
        if (returnType == Flux.class) return ReturnTypeStubs.class.getDeclaredMethod("fluxStub");
        return ReturnTypeStubs.class.getDeclaredMethod("longStub");
    }

    @SuppressWarnings("unused")
    private static class ReturnTypeStubs {
        Mono<?> monoStub() { return Mono.empty(); }
        Flux<?> fluxStub() { return Flux.empty(); }
        Long longStub() { return 0L; }
    }

    @Repository
    static class AnnotatedRepository {
        AnnotatedRepository() {}
    }

    @Repository
    interface AnnotatedInterface {}

    static class ImplWithAnnotatedInterface implements AnnotatedInterface {
        ImplWithAnnotatedInterface() {}
    }

    static class UnannotatedRepo {
        UnannotatedRepo() {}
    }
}
