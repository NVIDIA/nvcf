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
package com.nvidia.ess.encryption.util;

import io.micrometer.observation.Observation;
import io.micrometer.observation.contextpropagation.ObservationThreadLocalAccessor;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.handler.TracingObservationHandler.TracingContext;
import jakarta.annotation.Nullable;
import lombok.experimental.UtilityClass;
import reactor.util.context.ContextView;

@UtilityClass
public class TracingUtils {

    @Nullable
    private Span spanFromContext(ContextView ctx) {
        return ctx.<Observation>getOrEmpty(ObservationThreadLocalAccessor.KEY)
                .map(obs -> obs.getContextView().<TracingContext>get(TracingContext.class))
                .map(TracingContext::getSpan)
                .orElse(null);
    }

    public void setSpanAttribute(ContextView ctx, String key, String value) {
        var span = spanFromContext(ctx);
        if (span != null) {
            span.tag(key, value);
        }
    }

    public void recordException(ContextView ctx, Throwable throwable) {
        var span = spanFromContext(ctx);
        if (span != null) {
            span.error(throwable);
        }
    }
}
