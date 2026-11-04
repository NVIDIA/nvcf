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

import io.micrometer.common.KeyValue;
import io.micrometer.observation.Observation;
import io.micrometer.observation.ObservationHandler;
import io.micrometer.tracing.Tracer;
import io.micrometer.tracing.handler.DefaultTracingObservationHandler;
import java.util.Objects;
import org.apache.commons.lang3.StringUtils;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class ObservabilityConfig {

    /**
     * Preserves original span name casing. Workaround for
     * <a href="https://github.com/micrometer-metrics/tracing/issues/1092">micrometer-tracing#1092</a>;
     * remove when upgrading to micrometer-tracing 1.6+.
     *
     * <p>This is intentionally separate from {@link #threadAttributeObservationHandler()}.
     * {@code getSpanName} is called during {@code onStop} inside
     * {@code DefaultTracingObservationHandler}, and the default handler only runs for contexts
     * not already claimed in {@code FirstMatchingCompositeObservationHandler}
     */
    @Bean
    DefaultTracingObservationHandler defaultTracingObservationHandler(Tracer tracer) {
        return new DefaultTracingObservationHandler(tracer) {
            @Override
            public String getSpanName(Observation.Context context) {
                // implementation same as in the 1.6+ fix
                String name = context.getName();
                if (StringUtils.isNotBlank(context.getContextualName())) {
                    name = context.getContextualName();
                }
                return Objects.requireNonNull(name);
            }
        };
    }

    /** Adds {@code thread.id} and {@code thread.name} attributes to all spans.
     *
     * This handler must be a generic {@link ObservationHandler} so it fires for all observations.
     */
    @Bean
    ObservationHandler<Observation.Context> threadAttributeObservationHandler() {
        return new ObservationHandler<>() {
            @Override
            public void onStart(Observation.Context context) {
                Thread t = Thread.currentThread();
                context.addHighCardinalityKeyValue(KeyValue.of("thread.id", String.valueOf(t.threadId())));
                context.addHighCardinalityKeyValue(KeyValue.of("thread.name", t.getName()));
            }

            @Override
            public boolean supportsContext(Observation.Context context) {
                return true;
            }
        };
    }

}
