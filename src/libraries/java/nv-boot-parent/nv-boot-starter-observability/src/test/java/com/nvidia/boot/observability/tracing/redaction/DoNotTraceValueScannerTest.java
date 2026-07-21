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

package com.nvidia.boot.observability.tracing.redaction;

import static org.assertj.core.api.Assertions.assertThat;

import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;

class DoNotTraceValueScannerTest {

    private DoNotTraceValueScanner scanner;

    @BeforeEach
    void setUp() {
        scanner = new DoNotTraceValueScanner();
    }

    @Test
    @DisplayName("Returns empty when base package is blank")
    void returnsEmptyWhenPackageBlank() {
        assertThat(scanner.scan("")).isEmpty();
        assertThat(scanner.scan("   ")).isEmpty();
        assertThat(scanner.scan(null)).isEmpty();
    }

    @Test
    @DisplayName("Finds @DoNotTraceValue columns from @Table entities in scan package")
    void findsDoNotTraceValueColumns() {
        // Scan package containing TestTableEntity (in this test package)
        var columns = scanner.scan("com.nvidia.boot.observability.tracing.redaction");
        assertThat(columns).contains("credentials", "secret_token");
    }

    @Test
    @DisplayName("Returns empty when package has no @Table classes")
    void returnsEmptyWhenNoTableClasses() {
        var columns = scanner.scan("java.lang");
        assertThat(columns).isEmpty();
    }
}
