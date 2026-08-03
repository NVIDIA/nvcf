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

package com.nvidia.boot.observability.logging;

import static org.assertj.core.api.Assertions.assertThat;

import ch.qos.logback.classic.spi.ThrowableProxy;
import org.junit.jupiter.api.Test;

class StackFilteringThrowableConverterTest {

    @Test
    void producesValidStackTrace() {
        var converter = new TestableStackFilteringThrowableConverter();
        converter.start();

        var throwable = new RuntimeException("test");
        var proxy = new ThrowableProxy(throwable);

        var result = converter.convert(proxy);

        assertThat(result).contains("RuntimeException: test");
        assertThat(result).contains("StackFilteringThrowableConverterTest");
    }

    @Test
    void preservesNonFilteredFrames() {
        var converter = new TestableStackFilteringThrowableConverter();
        converter.start();

        var throwable = new RuntimeException("test");
        var proxy = new ThrowableProxy(throwable);

        var result = converter.convert(proxy);

        assertThat(result).contains("RuntimeException: test");
        assertThat(result).contains("StackFilteringThrowableConverterTest");
    }

    @Test
    void filtersMatchingFrames() {
        var converter = new TestableStackFilteringThrowableConverter();
        converter.start();

        var throwable = new RuntimeException("test");
        var proxy = new ThrowableProxy(throwable);

        var result = converter.convert(proxy);

        assertThat(result).contains("RuntimeException: test");
        assertThat(result).doesNotContain("sun.reflect.");
        assertThat(result).doesNotContain("java.lang.reflect.");
    }

    /**
     * Test subclass to expose protected throwableProxyToString for unit testing.
     */
    private static class TestableStackFilteringThrowableConverter
            extends StackFilteringThrowableConverter {
        String convert(ch.qos.logback.classic.spi.IThrowableProxy tp) {
            return throwableProxyToString(tp);
        }
    }
}
