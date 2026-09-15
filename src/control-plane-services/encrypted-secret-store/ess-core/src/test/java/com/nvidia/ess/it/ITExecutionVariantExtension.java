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
package com.nvidia.ess.it;

import java.lang.reflect.Method;
import java.util.List;
import java.util.stream.Stream;
import org.junit.jupiter.api.extension.BeforeTestExecutionCallback;
import org.junit.jupiter.api.extension.Extension;
import org.junit.jupiter.api.extension.ExtensionContext;
import org.junit.jupiter.api.extension.InvocationInterceptor;
import org.junit.jupiter.api.extension.ReflectiveInvocationContext;
import org.junit.jupiter.api.extension.TestTemplateInvocationContext;
import org.junit.jupiter.api.extension.TestTemplateInvocationContextProvider;

/**
 * Runs each opted-in test method once per {@link ITExecutionVariant} supplied by {@link #variants()},
 * setting the test instance's {@link IntegrationTestsBase#currentVariant} before the body runs so that
 * test-helpers (like {@link IntegrationTestsBase#effectiveNs(String)}), adjust their execution to suit
 * the current test-execution variant.
 * 
 * Usage instructions:
 *
 * <ul> 
 *    <li>Extend {@link ITExecutionVariantExtension} and override {@link ITExecutionVariantExtension#variants()}
 *        in the subclass (like {@link AuthDbStateVariantExtension}) to return one {@link ITExecutionVariant}
 *        instance for each variant of an annotated test-case (see below) that you wish to run.</li>.
 *    <li>Create an annotation to apply the extension (like {@link WithAuthDbStateVariants}).</li>
 *    <li>Apply the created annotation to the IT suite ({@code SpringBootTest}) whose test-cases
 *        need to have the desired variation. The IT suite must extend {@link IntegrationTestsBase}</li>
 *    <li>Ensure that all test-helpers in the IT suite whose execution needs to be adjusted to
 *        suit the current test-execution variant use {@link IntegrationTestsBase#currentVariant} to
 *        understand what the current test-execution variant should do.</li>
 *   <li>Annotate {@code Test}-annotated test-cases that need these variants with {@code @TestTemplate}
 *       instead.</li>
 *   <li>{@code @ParameterizedTest}-annotated test-cases should all use {@code @MethodSource} and the
 *       argument-provider should provide an argument-stream that is the cartesian product of the original
 *       argument-sequence and the {@link ITExecutionVariant} sequence from (your override of)
 *       {@link ITExecutionVariantExtension#variants()}.</li>
 * </ul>
 */
public abstract class ITExecutionVariantExtension
        implements TestTemplateInvocationContextProvider, InvocationInterceptor {

    protected abstract List<ITExecutionVariant> variants();

    @Override
    public boolean supportsTestTemplate(ExtensionContext context) {
        return context.getTestMethod().map(m -> m.getParameterCount() == 0).orElse(false);
    }

    @Override
    public Stream<TestTemplateInvocationContext> provideTestTemplateInvocationContexts(ExtensionContext context) {
        return variants().stream().map(VariantInvocationContext::new);
    }

    @Override
    public void interceptTestTemplateMethod(Invocation<Void> invocation,
            ReflectiveInvocationContext<Method> invocationContext, ExtensionContext extensionContext) throws Throwable {
        invocationContext.getArguments().stream()
                .filter(ITExecutionVariant.class::isInstance)
                .map(ITExecutionVariant.class::cast)
                .findFirst()
                .ifPresent(variant ->
                        ((IntegrationTestsBase) invocationContext.getTarget().orElseThrow()).currentVariant = variant);
        invocation.proceed();
    }

    private record VariantInvocationContext(ITExecutionVariant variant) implements TestTemplateInvocationContext {

        @Override
        public String getDisplayName(int invocationIndex) {
            return "[" + variant.displayName() + "]";
        }

        @Override
        public List<Extension> getAdditionalExtensions() {
            return List.of((BeforeTestExecutionCallback) ctx ->
                    ((IntegrationTestsBase) ctx.getRequiredTestInstance()).currentVariant = variant);
        }
    }
}
