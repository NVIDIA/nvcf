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
package com.nvidia.icms.outbound.sqs.model;

import static org.junit.jupiter.api.Assertions.*;

import org.junit.jupiter.api.Test;

class CapacityTypeTest {

    @Test
    void toString_returnsCorrectValues() {
        assertEquals("SPOT", CapacityType.SPOT.toString());
        assertEquals("RESERVED", CapacityType.RESERVED.toString());
        assertEquals("RESERVED_BACKUP", CapacityType.RESERVED_BACKUP.toString());
    }

    @Test
    void values_containsAllExpectedValues() {
        CapacityType[] values = CapacityType.values();

        assertEquals(3, values.length);
        assertEquals(CapacityType.SPOT, values[0]);
        assertEquals(CapacityType.RESERVED, values[1]);
        assertEquals(CapacityType.RESERVED_BACKUP, values[2]);
    }

    @Test
    void valueOf_returnsCorrectEnumValue() {
        assertEquals(CapacityType.SPOT, CapacityType.valueOf("SPOT"));
        assertEquals(CapacityType.RESERVED, CapacityType.valueOf("RESERVED"));
        assertEquals(CapacityType.RESERVED_BACKUP, CapacityType.valueOf("RESERVED_BACKUP"));
    }

    @Test
    void valueOf_throwsExceptionForInvalidValue() {
        assertThrows(IllegalArgumentException.class, () -> {
            CapacityType.valueOf("INVALID_TYPE");
        });
    }
}
