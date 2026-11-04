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
package com.nvidia.icms.uec.internal.util;

import lombok.experimental.UtilityClass;

@UtilityClass
public class NumericalRepresentationUtils {

    /**
     * @return string hex code representation with '0x' prefix (e.g. `0x8A8C0004`).
     */
    public static String hexCode(int value) {
        return String.format("0x%08X", value);
    }

    /**
     * @return string hex code representation without '0x' prefix (e.g. `8A8C0004`).
     */
    public static String hexCodeShort(int value) {
        return String.format("%08X", value);
    }

    /**
     * Converts results from {@link #hexCode(int)} or {@link #hexCodeShort(int)}
     * back into int value. This method is case-insensitive.
     */
    public static int fromHexCode(String hexCode) {
        if (hexCode.regionMatches(true, 0, "0x", 0, 2)) {
            hexCode = hexCode.substring(2);
        }

        return Integer.parseUnsignedInt(hexCode, 16);
    }

    /**
     * 0 -> "0x0"
     * 15 -> "0xF"
     * 256 -> "0x100"
     * -1 -> "0xFFFFFFFF"
     *
     * @return similar to {{@link #hexCode(int)}}, but with trailing zeros removed.
     */
    public static String trimmedHexCode(int value) {
        return "0x" + trimmedHexCodeShort(value);
    }

    /**
     * 0 -> "0"
     * 15 -> "F"
     * 256 -> "100"
     * -1 -> "FFFFFFFF"
     *
     * @return similar to {{@link #hexCodeShort(int)}, but with trailing zeros removed.
     */
    public static String trimmedHexCodeShort(int value) {
        return Integer.toHexString(value).toUpperCase();
    }
}
