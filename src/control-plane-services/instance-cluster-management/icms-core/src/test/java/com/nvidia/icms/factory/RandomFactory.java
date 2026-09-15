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
package com.nvidia.icms.factory;

import jakarta.annotation.Nullable;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import org.jetbrains.annotations.NotNull;

/**
 * Factory class for generating random test data strings.
 * Used for creating test identifiers and mock data in unit tests.
 */
public class RandomFactory {

    /**
     * Generates a list of random strings with an optional prefix.
     * Each string in the list will have format: "RAND_prefix_randomString"
     *
     * @param count number of random strings to generate
     * @param prefix optional prefix to prepend to each string (can be null)
     * @return List of random strings with the specified format
     */
    public static @NotNull List<String> getRandomStringList(int count, @Nullable String prefix) {
        ArrayList<String> result = new ArrayList<>();

        if (prefix == null) {
            prefix = "";
        }
        for (int i = 0; i < count; i++) {
            result.add(getRandomStringWithPrefix(prefix, 5));
        }

        return result;
    }

    /**
     * Creates a random string with a specified prefix and fixed format.
     * Output format: "RAND_prefix_randomString"
     *
     * @param prefix prefix to prepend to the random string
     * @param length length of the random portion of the string
     * @return Formatted random string
     */
    public static @NotNull String getRandomStringWithPrefix(@NotNull String prefix, int length) {
        return "RAND_" + prefix + "_" + getRandomString(length);
    }

    /**
     * Generates a random string of specified length using UUID.
     * Removes underscores from UUID and truncates to desired length.
     *
     * @param length desired length of output string
     * @return Random string of specified length
     */
    public static @NotNull String getRandomString(int length) {
        return UUID.randomUUID().toString().replace("_", "").substring(0, length);
    }

}
