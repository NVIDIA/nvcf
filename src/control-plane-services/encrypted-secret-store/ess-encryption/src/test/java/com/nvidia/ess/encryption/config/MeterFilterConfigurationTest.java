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
package com.nvidia.ess.encryption.config;

import static com.nvidia.ess.encryption.constants.Constants.TRACE_ONLY_NAME;
import static org.assertj.core.api.Assertions.assertThat;

import io.micrometer.core.instrument.Meter;
import io.micrometer.core.instrument.Tags;
import io.micrometer.core.instrument.config.MeterFilter;
import io.micrometer.core.instrument.config.MeterFilterReply;
import org.junit.jupiter.api.Test;

class MeterFilterConfigurationTest {

    private final MeterFilterConfiguration configuration = new MeterFilterConfiguration();

    @Test
    void deniesTheBaseTraceOnlyName() {
        MeterFilter filter = configuration.suppressTraceOnlyObservations();
        Meter.Id id = new Meter.Id(TRACE_ONLY_NAME, Tags.empty(), null, null, Meter.Type.COUNTER);

        assertThat(filter.accept(id)).isEqualTo(MeterFilterReply.DENY);
    }

    @Test
    void deniesTheActiveTraceOnlyName() {
        MeterFilter filter = configuration.suppressTraceOnlyObservations();
        Meter.Id id = new Meter.Id(TRACE_ONLY_NAME + ".active", Tags.empty(), null, null, Meter.Type.COUNTER);

        assertThat(filter.accept(id)).isEqualTo(MeterFilterReply.DENY);
    }

    @Test
    void acceptsAnUnrelatedMeterName() {
        MeterFilter filter = configuration.suppressTraceOnlyObservations();
        Meter.Id id = new Meter.Id("http.server.requests", Tags.empty(), null, null, Meter.Type.COUNTER);

        assertThat(filter.accept(id)).isEqualTo(MeterFilterReply.NEUTRAL);
    }
}
