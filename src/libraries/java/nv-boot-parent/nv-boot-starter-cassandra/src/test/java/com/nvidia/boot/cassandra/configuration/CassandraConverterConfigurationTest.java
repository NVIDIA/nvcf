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

package com.nvidia.boot.cassandra.configuration;

import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.oss.driver.api.core.data.CqlDuration;
import java.time.Duration;
import org.junit.jupiter.api.Test;
import org.springframework.core.convert.support.GenericConversionService;

class CassandraConverterConfigurationTest {

    private final CassandraConverterConfiguration config = new CassandraConverterConfiguration();

    @Test
    void cassandraCustomConversionsReturnsNonNullBean() {
        var conversions = config.cassandraCustomConversions();
        assertThat(conversions).isNotNull();
    }

    @Test
    void cassandraCustomConversionsRegistersDurationWriteConverter() {
        var conversions = config.cassandraCustomConversions();
        assertThat(conversions.hasCustomWriteTarget(Duration.class)).isTrue();
    }

    @Test
    void cassandraCustomConversionsRegistersCqlDurationReadConverter() {
        var conversions = config.cassandraCustomConversions();
        assertThat(conversions.hasCustomReadTarget(CqlDuration.class, Duration.class)).isTrue();
    }

    @Test
    void durationToCqlDurationConvertsCorrectly() {
        var conversions = config.cassandraCustomConversions();
        var conversionService = new GenericConversionService();
        conversions.registerConvertersIn(conversionService);

        var duration = Duration.ofSeconds(30).plusNanos(500_000_000);
        var cqlDuration = conversionService.convert(duration, CqlDuration.class);

        assertThat(cqlDuration).isNotNull();
        assertThat(cqlDuration.getDays()).isZero();
        assertThat(cqlDuration.getMonths()).isZero();
        assertThat(cqlDuration.getNanoseconds()).isEqualTo(30_500_000_000L);
    }

    @Test
    void cqlDurationToDurationConvertsCorrectly() {
        var conversions = config.cassandraCustomConversions();
        var conversionService = new GenericConversionService();
        conversions.registerConvertersIn(conversionService);

        // CqlDuration(months, days, nanoseconds) - converter uses days and nanoseconds only
        var cqlDuration = CqlDuration.newInstance(0, 1, 5_000_000_000L);
        var duration = conversionService.convert(cqlDuration, Duration.class);

        assertThat(duration).isNotNull();
        assertThat(duration).isEqualTo(Duration.ofDays(1).plusNanos(5_000_000_000L));
    }

    @Test
    void durationRoundTripPreservesValue() {
        var conversions = config.cassandraCustomConversions();
        var conversionService = new GenericConversionService();
        conversions.registerConvertersIn(conversionService);

        var original = Duration.ofHours(2).plusMinutes(30).plusNanos(123);
        var cqlDuration = conversionService.convert(original, CqlDuration.class);
        var roundTripped = conversionService.convert(cqlDuration, Duration.class);

        assertThat(roundTripped).isEqualTo(original);
    }
}
