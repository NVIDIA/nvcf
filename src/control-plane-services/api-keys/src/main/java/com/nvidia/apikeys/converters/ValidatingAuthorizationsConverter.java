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

package com.nvidia.apikeys.converters;

import tools.jackson.databind.JsonNode;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.boot.exceptions.BadRequestException;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Service
public class ValidatingAuthorizationsConverter {

    public String readValidAuthorizations(ServiceVo service, JsonNode authorizationsNode) {
        if (authorizationsNode == null
                || authorizationsNode.isMissingNode() || authorizationsNode.isNull()) {
            throw new BadRequestException("authorizations can not be null");
        }
        String authorizations = authorizationsNode.toString();
        int authorizationsLength = StringUtils.length(authorizations);
        if (authorizationsLength > service.getMaxAuthzSizeChars()) {
            throw new BadRequestException(
                    String.format("authorizations size can not be greater than %d but was %d",
                                  service.getMaxAuthzSizeChars(), authorizationsLength));
        }
        return authorizations;
    }


}
