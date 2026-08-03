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

import com.datastax.oss.driver.api.core.data.CqlDuration;
import java.time.Duration;
import java.util.List;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.convert.converter.Converter;
import org.springframework.data.cassandra.core.convert.CassandraCustomConversions;
import org.springframework.data.convert.ReadingConverter;
import org.springframework.data.convert.WritingConverter;

@Configuration
public class CassandraConverterConfiguration {

    @ReadingConverter
    private static class CqlDurationToDurationConverter
            implements Converter<CqlDuration, Duration> {
        @Override
        public Duration convert(CqlDuration source) {
            return Duration
                    .ofNanos(source.getNanoseconds())
                    .plusDays(source.getDays());
        }
    }

    @WritingConverter
    private static class DurationToCqlDurationConverter
            implements Converter<Duration, CqlDuration> {
        @Override
        public CqlDuration convert(Duration source) {
            return CqlDuration.newInstance(0, 0, source.toNanos());
        }
    }

    @Bean
    public CassandraCustomConversions cassandraCustomConversions() {
        return new CassandraCustomConversions(List.of(new DurationToCqlDurationConverter(),
                                                      new CqlDurationToDurationConverter()));
    }
}
