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
package com.nvidia.ess.it.notary;

import static org.springframework.http.HttpHeaders.CONTENT_TYPE;
import static org.springframework.http.MediaType.APPLICATION_PROBLEM_JSON;

import tools.jackson.databind.ObjectMapper;
import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.http.ContentTypeHeader;
import com.github.tomakehurst.wiremock.http.HttpHeaders;
import com.github.tomakehurst.wiremock.http.Request;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.ess.exceptions.InternalErrorException;
import com.nvidia.ess.it.notary.model.AssertionRequest;
import com.nvidia.ess.it.notary.model.AssertionResponse;
import java.net.URI;
import lombok.SneakyThrows;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.JwtException;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;

public class AssertionEndpointResponseTransformer implements ResponseTransformerV2 {

    public static final String NAME = "jwt-generator";
    private final ObjectMapper objectMapper = new ObjectMapper();
    private final NotaryJwtGenerator notaryJwtGenerator;
    private final String notarySignAuthOauth2JwksEndpoint;

    public AssertionEndpointResponseTransformer(NotaryJwtGenerator notaryJwtGenerator,
            String notarySignAuthOauth2BaseUrl) {
        this.notaryJwtGenerator = notaryJwtGenerator;
        this.notarySignAuthOauth2JwksEndpoint = notarySignAuthOauth2BaseUrl + "/.well-known/jwks.json";
    }

    private void verifyOauth2JWTSignature(SignedJWT token) throws BootResponseException {

        try {

            var algorithm = token.getHeader().getAlgorithm().getName();
            NimbusJwtDecoder.withJwkSetUri(notarySignAuthOauth2JwksEndpoint)
                    .jwsAlgorithm(SignatureAlgorithm.from(algorithm))
                    .build()
                    .decode(token.getParsedString());

        } catch (JwtException ex) {
            throw new UnauthorizedException("JWT signature could not be verified", ex);
        } catch (Throwable ex) {
            throw new InternalErrorException("Unable to verify JWT signature due to unhandled error", ex);
        }
    }

    @Override
    public String getName() {
        return NAME;
    }

    @Override
    @SneakyThrows
    public Response transform(Response response, ServeEvent serveEvent) {
        var request = serveEvent.getRequest();
        var authorization = request.getHeader("Authorization");
        var tokenString = authorization.replace("Bearer ", "");
        var token = SignedJWT.parse(tokenString);

        try {
            verifyOauth2JWTSignature(token);
        } catch (BootResponseException ex) {

            var headers = new HttpHeaders(response.getHeaders()
                            .all()
                            .stream()
                            .filter(h -> !h.keyEquals(CONTENT_TYPE))
                            .toList())
                    .plus(new ContentTypeHeader(APPLICATION_PROBLEM_JSON.toString()));

            return Response.response()
                    .status(ex.getStatusCode().value())
                    .headers(headers)
                    .body(objectMapper.writeValueAsBytes(ex.getBody()))
                    .build();
        }

        var sub = token.getJWTClaimsSet().getSubject();

        var body = getAssertionRequestBody(request);
        var baseUrl = request.getAbsoluteUrl()
                .substring(0, request.getAbsoluteUrl().length() - request.getUrl().length());

        var jwt = notaryJwtGenerator.getJwt(sub, URI.create(baseUrl).toURL(), body.getAudienceServiceIds(), body.getData());
        var notaryResponse = AssertionResponse.builder().assertion(jwt).build();
        return Response.response()
                .status(response.getStatus())
                .headers(response.getHeaders())
                .body(objectMapper.writeValueAsBytes(notaryResponse))
                .build();
    }

    @Override
    public boolean applyGlobally() {
        return false;
    }

    @SneakyThrows
    private AssertionRequest getAssertionRequestBody(Request request) {
        return objectMapper.readValue(request.getBody(), AssertionRequest.class);
    }
}
