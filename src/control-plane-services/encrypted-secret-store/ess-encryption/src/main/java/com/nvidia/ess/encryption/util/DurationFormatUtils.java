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

import static org.apache.commons.lang3.time.DurationFormatUtils.formatDuration;

import lombok.experimental.UtilityClass;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.Strings;

/**
 * Copied from {@link org.apache.commons.lang3.time.DurationFormatUtils#formatDurationWords(long, boolean, boolean)}
 * and modified to include milliseconds
 */
@UtilityClass
public class DurationFormatUtils {
    public String formatDurationWords(
            final long durationMillis) {

        // This method is generally replaceable by the format method, but
        // there are a series of tweaks and special cases that require
        // trickery to replicate.
        String duration = formatDuration(durationMillis, "d' days 'H' hours 'm' minutes 's' seconds 'SSS' milliseconds'", false);
        // this is a temporary marker on the front. Like ^ in regexp.
        duration = " " + duration;
        String tmp = Strings.CS.replaceOnce(duration, " 0 days", StringUtils.EMPTY);
        if (tmp.length() != duration.length()) {
            duration = tmp;
            tmp = Strings.CS.replaceOnce(duration, " 0 hours", StringUtils.EMPTY);
            if (tmp.length() != duration.length()) {
                duration = tmp;
                tmp = Strings.CS.replaceOnce(duration, " 0 minutes", StringUtils.EMPTY);
                if (tmp.length() != duration.length()) {
                    duration = tmp;
                    tmp = Strings.CS.replaceOnce(duration, " 0 seconds", StringUtils.EMPTY);
                    duration = tmp;
                }
            }
        }
        if (!duration.isEmpty()) {
            // strip the space off again
            duration = duration.substring(1);
        }
        tmp = Strings.CS.replaceOnce(duration, " 000 milliseconds", StringUtils.EMPTY);
        if (tmp.length() != duration.length()) {
            duration = tmp;
            tmp = Strings.CS.replaceOnce(duration, " 0 seconds", StringUtils.EMPTY);
            if (tmp.length() != duration.length()) {
                duration = tmp;
                tmp = Strings.CS.replaceOnce(duration, " 0 minutes", StringUtils.EMPTY);
                if (tmp.length() != duration.length()) {
                    duration = tmp;
                    tmp = Strings.CS.replaceOnce(duration, " 0 hours", StringUtils.EMPTY);
                    if (tmp.length() != duration.length()) {
                        duration = Strings.CS.replaceOnce(tmp, " 0 days", StringUtils.EMPTY);
                    }
                }
            }
        }
        // handle plurals
        duration = " " + duration;
        duration = Strings.CS.replaceOnce(duration, " 001 milliseconds", " 001 millisecond");
        duration = Strings.CS.replaceOnce(duration, " 1 seconds", " 1 second");
        duration = Strings.CS.replaceOnce(duration, " 1 minutes", " 1 minute");
        duration = Strings.CS.replaceOnce(duration, " 1 hours", " 1 hour");
        duration = Strings.CS.replaceOnce(duration, " 1 days", " 1 day");
        return duration.trim();
    }
}
