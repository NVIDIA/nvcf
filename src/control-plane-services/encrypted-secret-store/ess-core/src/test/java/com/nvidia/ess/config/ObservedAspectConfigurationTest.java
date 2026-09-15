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
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import com.nvidia.ess.config.ObservedAspectConfiguration.ObservedMethodAspect;
import io.micrometer.common.KeyValue;
import io.micrometer.core.instrument.Meter.Id;
import io.micrometer.core.instrument.Meter.Type;
import io.micrometer.core.instrument.Tags;
import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.core.instrument.config.MeterFilterReply;
import io.micrometer.observation.Observation;
import io.micrometer.observation.ObservationHandler;
import io.micrometer.observation.ObservationRegistry;
import io.micrometer.observation.annotation.Observed;
import java.lang.reflect.Method;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import org.aspectj.lang.ProceedingJoinPoint;
import org.aspectj.lang.reflect.MethodSignature;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;
import org.mockito.junit.jupiter.MockitoExtension;
import org.reactivestreams.Publisher;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class ObservedAspectConfigurationTest {

    private final ObservedAspectConfiguration config = new ObservedAspectConfiguration();
    private ObservationRegistry registry;
    private final List<Observation.Context> startedObservations = new ArrayList<>();

    @BeforeEach
    void setUp() {
        registry = ObservationRegistry.create();
        registry.observationConfig().observationHandler(new ObservationHandler<>() {
            @Override
            public boolean supportsContext(Observation.Context context) {
                return true;
            }

            @Override
            public void onStart(Observation.Context context) {
                startedObservations.add(context);
            }
        });
    }

    // --- Bean creation ---

    @Test
    void observedMethodAspect_shouldReturnAspect() {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);

        assertThat(aspect).isNotNull();
    }

    @Test
    void suppressTraceOnlyObservationMetrics_shouldReturnMeterFilter() {
        MeterFilter filter = config.suppressTraceOnlyObservationMetrics();

        assertThat(filter).isNotNull();
    }

    // --- Constant ---

    @Test
    void traceOnlyName_shouldMatchExpectedValue() {
        assertThat(ObservedAspectConfiguration.TRACE_ONLY_NAME).isEqualTo("method.trace.only");
    }

    // --- MeterFilter ---

    @ParameterizedTest
    @ValueSource(strings = {"method.trace.only", "method.trace.only.active"})
    void suppressTraceOnlyObservationMetrics_shouldDenySuppressedMetrics(String metricName) {
        MeterFilter filter = config.suppressTraceOnlyObservationMetrics();
        Id id = new Id(metricName, Tags.empty(), null, null, Type.COUNTER);

        MeterFilterReply reply = filter.accept(id);

        assertThat(reply).isEqualTo(MeterFilterReply.DENY);
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "http.server.requests", "method.observed", "jvm.memory.used",
            "method.trace.only.something"
    })
    void suppressTraceOnlyObservationMetrics_shouldAllowOtherMetrics(String metricName) {
        MeterFilter filter = config.suppressTraceOnlyObservationMetrics();
        Id id = new Id(metricName, Tags.empty(), null, null, Type.COUNTER);

        MeterFilterReply reply = filter.accept(id);

        assertThat(reply).isEqualTo(MeterFilterReply.NEUTRAL);
    }

    // --- Sync return type ---

    @Test
    void observe_sync_shouldReturnProceedResult() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("defaultSync", "hello");

        Object result = aspect.observe(pjp);

        assertThat(result).isEqualTo("hello");
    }

    @Test
    void observe_sync_shouldPropagateException() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        RuntimeException error = new RuntimeException("sync error");
        ProceedingJoinPoint pjp = mockNonReactivePjpThrowing("defaultSync", error);

        assertThatThrownBy(() -> aspect.observe(pjp)).isSameAs(error);
    }

    // --- Mono return type ---

    @Test
    @SuppressWarnings("unchecked")
    void observe_mono_shouldReturnWrappedMono() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockReactivePjp("monoMethod", Mono.just("value"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create((Mono<String>) result)
                .expectNext("value")
                .verifyComplete();
    }

    @Test
    void observe_mono_shouldHandleEmpty() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockReactivePjp("monoMethod", Mono.empty());

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create((Mono<?>) result).verifyComplete();
    }

    @Test
    void observe_mono_shouldPropagateError() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        RuntimeException error = new RuntimeException("mono error");
        ProceedingJoinPoint pjp = mockReactivePjp("monoMethod", Mono.error(error));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Mono.class);
        StepVerifier.create((Mono<?>) result)
                .expectErrorSatisfies(e -> assertThat(e).isSameAs(error))
                .verify();
    }

    // --- Flux return type ---

    @Test
    @SuppressWarnings("unchecked")
    void observe_flux_shouldReturnWrappedFlux() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockReactivePjp("fluxMethod", Flux.just("a", "b", "c"));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Flux.class);
        StepVerifier.create((Flux<String>) result)
                .expectNext("a", "b", "c")
                .verifyComplete();
    }

    @Test
    void observe_flux_shouldPropagateError() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        RuntimeException error = new RuntimeException("flux error");
        ProceedingJoinPoint pjp = mockReactivePjp("fluxMethod", Flux.error(error));

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(Flux.class);
        StepVerifier.create((Flux<?>) result)
                .expectErrorSatisfies(e -> assertThat(e).isSameAs(error))
                .verify();
    }

    // --- CompletionStage return type ---

    @Test
    void observe_completionStage_shouldReturnResult() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        CompletableFuture<String> future = CompletableFuture.completedFuture("done");
        ProceedingJoinPoint pjp = mockNonReactivePjp("completionStageMethod", future);

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(CompletionStage.class);
        assertThat(((CompletionStage<?>) result).toCompletableFuture().join()).isEqualTo("done");
    }

    @Test
    void observe_completionStage_shouldHandleFailedStage() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        CompletableFuture<String> future =
                CompletableFuture.failedFuture(new RuntimeException("cs error"));
        ProceedingJoinPoint pjp = mockNonReactivePjp("completionStageMethod", future);

        Object result = aspect.observe(pjp);

        assertThat(result).isInstanceOf(CompletionStage.class);
        assertThat(((CompletionStage<?>) result).toCompletableFuture())
                .isCompletedExceptionally();
    }

    @Test
    void observe_completionStage_shouldHandleNullFromProceed() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("completionStageMethod", null);

        Object result = aspect.observe(pjp);

        assertThat(result).isNull();
    }

    @Test
    void observe_completionStage_shouldPropagateExceptionFromProceed() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        RuntimeException error = new RuntimeException("proceed error");
        ProceedingJoinPoint pjp = mockNonReactivePjpThrowing("completionStageMethod", error);

        assertThatThrownBy(() -> aspect.observe(pjp)).isSameAs(error);
    }

    // --- Publisher that is neither Mono nor Flux ---

    @Test
    @SuppressWarnings("unchecked")
    void observe_publisher_shouldReturnAsIsWhenNotMonoOrFlux() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        Publisher<String> rawPublisher = mock(Publisher.class);
        ProceedingJoinPoint pjp = mockReactivePjp("rawPublisher", rawPublisher);

        Object result = aspect.observe(pjp);

        assertThat(result).isSameAs(rawPublisher);
    }

    // --- Observation name resolution ---

    @Test
    void observe_shouldDefaultNameToMethodObserved() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("defaultSync", "result");

        aspect.observe(pjp);

        assertThat(startedObservations).hasSize(1);
        assertThat(startedObservations.getFirst().getName()).isEqualTo("method.observed");
    }

    @Test
    void observe_shouldUseCustomName() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("customNameSync", "result");

        aspect.observe(pjp);

        assertThat(startedObservations).hasSize(1);
        assertThat(startedObservations.getFirst().getName()).isEqualTo("custom.name");
    }

    // --- Observation contextualName resolution ---

    @Test
    void observe_shouldDefaultContextualNameToClassDotMethod() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("defaultSync", "result");

        aspect.observe(pjp);

        assertThat(startedObservations).hasSize(1);
        assertThat(startedObservations.getFirst().getContextualName())
                .isEqualTo("ObservedStubs.defaultSync");
    }

    @Test
    void observe_shouldUseCustomContextualName() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("customContextualSync", "result");

        aspect.observe(pjp);

        assertThat(startedObservations).hasSize(1);
        assertThat(startedObservations.getFirst().getContextualName())
                .isEqualTo("Custom.operation");
    }

    // --- High/low cardinality key values ---

    @Test
    void observe_shouldSetCodeNamespaceAndFunction() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("defaultSync", "result");

        aspect.observe(pjp);

        assertThat(startedObservations).hasSize(1);
        Observation.Context ctx = startedObservations.getFirst();
        assertThat(ctx.getHighCardinalityKeyValues())
                .contains(
                        KeyValue.of("code.namespace", ObservedStubs.class.getName()),
                        KeyValue.of("code.function", "defaultSync"));
    }

    @Test
    void observe_shouldPassLowCardinalityKeyValues() throws Throwable {
        ObservedMethodAspect aspect = config.observedMethodAspect(registry);
        ProceedingJoinPoint pjp = mockNonReactivePjp("withKeyValues", "result");

        aspect.observe(pjp);

        assertThat(startedObservations).hasSize(1);
        Observation.Context ctx = startedObservations.getFirst();
        assertThat(ctx.getLowCardinalityKeyValues())
                .contains(
                        KeyValue.of("env", "test"),
                        KeyValue.of("region", "us"));
    }

    // --- Test helpers ---

    private ProceedingJoinPoint mockPjp(String stubMethodName, Object returnValue,
            boolean stubSignatureReturnType) throws Throwable {
        ProceedingJoinPoint pjp = mock(ProceedingJoinPoint.class);
        MethodSignature sig = mock(MethodSignature.class);
        Method method = ObservedStubs.class.getDeclaredMethod(stubMethodName);

        when(pjp.getSignature()).thenReturn(sig);
        when(sig.getMethod()).thenReturn(method);
        lenient().when(sig.getDeclaringType()).thenReturn(ObservedStubs.class);
        when(sig.getDeclaringTypeName()).thenReturn(ObservedStubs.class.getName());
        when(sig.getName()).thenReturn(stubMethodName);
        when(pjp.proceed()).thenReturn(returnValue);

        if (stubSignatureReturnType) {
            when(sig.getReturnType()).thenReturn(method.getReturnType());
        }

        return pjp;
    }

    private ProceedingJoinPoint mockNonReactivePjp(String stubMethodName, Object returnValue)
            throws Throwable {
        return mockPjp(stubMethodName, returnValue, true);
    }

    private ProceedingJoinPoint mockReactivePjp(String stubMethodName, Object returnValue)
            throws Throwable {
        return mockPjp(stubMethodName, returnValue, false);
    }

    private ProceedingJoinPoint mockNonReactivePjpThrowing(String stubMethodName, Throwable error)
            throws Throwable {
        ProceedingJoinPoint pjp = mock(ProceedingJoinPoint.class);
        MethodSignature sig = mock(MethodSignature.class);
        Method method = ObservedStubs.class.getDeclaredMethod(stubMethodName);

        when(pjp.getSignature()).thenReturn(sig);
        when(sig.getMethod()).thenReturn(method);
        lenient().when(sig.getDeclaringType()).thenReturn(ObservedStubs.class);
        when(sig.getDeclaringTypeName()).thenReturn(ObservedStubs.class.getName());
        when(sig.getName()).thenReturn(stubMethodName);
        when(pjp.proceed()).thenThrow(error);
        when(sig.getReturnType()).thenReturn(method.getReturnType());

        return pjp;
    }

    @SuppressWarnings("unused")
    static class ObservedStubs {
        @Observed
        String defaultSync() { return ""; }

        @Observed(name = "custom.name")
        String customNameSync() { return ""; }

        @Observed(contextualName = "Custom.operation")
        String customContextualSync() { return ""; }

        @Observed(lowCardinalityKeyValues = {"env", "test", "region", "us"})
        String withKeyValues() { return ""; }

        @Observed
        Mono<String> monoMethod() { return Mono.empty(); }

        @Observed
        Flux<String> fluxMethod() { return Flux.empty(); }

        @Observed
        CompletionStage<String> completionStageMethod() { return CompletableFuture.completedFuture(""); }

        @Observed
        Publisher<String> rawPublisher() { return Mono.empty(); }
    }
}
