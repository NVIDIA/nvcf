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
package com.nvidia.ess.validator;

import java.util.Objects;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.web.firewall.StrictHttpFirewall;
import org.springframework.stereotype.Component;

@Slf4j
@Component
public class NotBlankAndUriSafeValidationHelper {

    @Setter(onMethod_ = @Autowired)
    private StrictHttpFirewall httpFirewall;

    /**
     * 
     * Given both the URL-decoded form ({@code urlDecodedString}) and the raw-form ({@code rawString}) of all
     * or a part of an API URI path (or a string that needs to be checked for whether it can legally fit into an
     * API URI), validate whether the string is not blank and URI-safe.
     * 
     * @param urlDecodedString
     * @param rawString
     * @return {@code true} if validation was successful, {@code false} otherwise.
     */
    public boolean notBlankAndUriSafe(String urlDecodedString, String rawString) {

        if (StringUtils.isBlank(urlDecodedString) || StringUtils.isBlank(rawString)) {
            return false;
        }

        var decodedUrlBlocklist = httpFirewall.getDecodedUrlBlocklist();

        for (var blocklistedString : decodedUrlBlocklist) {
            if (!Objects.isNull(blocklistedString) && urlDecodedString.contains(blocklistedString)) {
                return false;
            }
        }

        var encodedUrlBlocklist = httpFirewall.getEncodedUrlBlocklist();

        for (var encodedBlocklistedString : encodedUrlBlocklist) {
            if (!Objects.isNull(encodedBlocklistedString) && rawString.contains(encodedBlocklistedString)) {
                return false;
            }
        }

        return true;
    }
}
