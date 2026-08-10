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

package com.nvidia.boot.mock.oauth2;

import com.github.tomakehurst.wiremock.common.Urls;
import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.http.Request;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import java.net.URI;
import java.util.Arrays;
import java.util.Base64;
import lombok.SneakyThrows;
import tools.jackson.databind.json.JsonMapper;

/**
 * WireMock response transformer for /token endpoint.
 */
public class TokenEndpointResponseTransformer implements ResponseTransformerV2 {

    public static final String NAME = "jwt-generator";
    private final JsonMapper jsonMapper = JsonMapper.builder().build();

    @Override
    public String getName() {
        return NAME;
    }

    @Override
    @SneakyThrows
    public Response transform(Response response, ServeEvent serveEvent) {
        var request = serveEvent.getRequest();
        var authorization = request.getHeader("Authorization");
        var decoded = new String(
                Base64.getUrlDecoder().decode(authorization.substring("Basic ".length())));
        var clientID = decoded.split(":", 2)[0];
        var body = new String(request.getBody());
        var params = Urls.splitQuery(body);
        var scopes = Arrays.asList(params.get("scope").firstValue().split(" "));
        var baseUrl = request.getAbsoluteUrl()
                .substring(0, request.getAbsoluteUrl().length() - request.getUrl().length());
        var options = getOptionsQueryParameter(request);
        var jwt = OAuth2TestUtils.getJwt(clientID, scopes, 3600,
                                         URI.create(baseUrl).toURL(), options);
        var tokenResponse = OAuth2TokenServerResponse.builder().accessToken(jwt).expiresIn(3600L)
                .scope(params.get("scope").firstValue()).tokenType("Bearer").build();
        return Response.response()
                .status(response.getStatus())
                .headers(response.getHeaders())
                .body(jsonMapper.writeValueAsBytes(tokenResponse))
                .build();
    }

    @Override
    public boolean applyGlobally() {
        return false;
    }

    private String getOptionsQueryParameter(Request request) {
        var optionsParameter = request.queryParameter("options");
        if (optionsParameter.isPresent()) {
            return optionsParameter.firstValue();
        }

        var body = request.getBodyAsString();
        var nameValuePairs = body.split("&");
        for (String nameValuePair : nameValuePairs) {
            if (nameValuePair.startsWith("options=")) {
                return nameValuePair.substring("options=".length());
            }
        }

        return null;
    }
}
