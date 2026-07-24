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
package com.nvidia.nvct.service.ngc;

import static org.springframework.http.HttpHeaders.AUTHORIZATION;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvct.service.ngc.dto.CreateModelRequest;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.service.annotation.DeleteExchange;
import org.springframework.web.service.annotation.PostExchange;

// Used to validate the NGC_API_KEY and the org/team names that are provided
// during Task creation so that we can fail fast. We don't care about the
// response from these endpoints. We just care about the response status code
// and the error message, if any.
//
// There is no interceptor involved in this case as NGC_API_KEY provided by
// the user during Task creation serves as the Bearer token.
public interface NgcRegistryStubService {

    @PostExchange(url = "/v2/org/{orgName}/models", contentType = APPLICATION_JSON_VALUE)
    ResponseEntity<Void> createOrgModel(
            @RequestHeader(name = AUTHORIZATION) String authorization,
            @PathVariable String orgName,
            @RequestBody CreateModelRequest payload);

    @PostExchange(url = "/v2/org/{orgName}/team/{teamName}/models",
                  contentType = APPLICATION_JSON_VALUE)
    ResponseEntity<Void> createTeamModel(
            @RequestHeader(name = AUTHORIZATION) String authorization,
            @PathVariable String orgName,
            @PathVariable String teamName,
            @RequestBody CreateModelRequest payload);

    @DeleteExchange("/v2/org/{orgName}/models/{modelName}")
    ResponseEntity<Void> deleteOrgModel(
            @RequestHeader(name = AUTHORIZATION) String authorization,
            @PathVariable String orgName,
            @PathVariable String modelName);

    @DeleteExchange("/v2/org/{orgName}/team/{teamName}/models/{modelName}")
    ResponseEntity<Void> deleteTeamModel(
            @RequestHeader(name = AUTHORIZATION) String authorization,
            @PathVariable String orgName,
            @PathVariable String teamName,
            @PathVariable String modelName);
}
