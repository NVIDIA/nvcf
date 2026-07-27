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
package com.nvidia.nvct.util;

import static com.nvidia.boot.mock.oauth2.OAuth2TestUtils.getServiceId;
import static net.minidev.json.parser.JSONParser.DEFAULT_PERMISSIVE_MODE;

import com.github.tomakehurst.wiremock.extension.ResponseTransformerV2;
import com.github.tomakehurst.wiremock.http.Request;
import com.github.tomakehurst.wiremock.http.Response;
import com.github.tomakehurst.wiremock.stubbing.ServeEvent;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvct.service.token.client.NotaryStubService.SecretPathsAssertion;
import com.nvidia.nvct.service.token.client.NotaryStubService.SignResponse;
import com.nvidia.nvct.service.token.client.NotaryStubService.SignSecretPathsRequest;
import com.nvidia.nvct.service.token.client.NotaryStubService.SignWorkerAccessRequest;
import com.nvidia.nvct.service.token.client.NotaryStubService.WorkerAccessAssertion;
import java.net.URI;
import java.net.URL;
import java.security.KeyPairGenerator;
import java.security.interfaces.ECPrivateKey;
import java.security.interfaces.ECPublicKey;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.UUID;
import lombok.Getter;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import net.minidev.json.parser.JSONParser;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
public class NotaryServiceResponseTransformer implements ResponseTransformerV2 {

    public static final String NAME = "notary-service-response-transformer";

    private final JsonMapper jsonMapper = JsonMapper.builder().build();

    private String notaryBaseUrl;
    private String notaryClientId;

    public NotaryServiceResponseTransformer(String notaryBaseUrl, String notaryClientId) {
        this.notaryBaseUrl = notaryBaseUrl;
        this.notaryClientId = notaryClientId;
    }

    public String getJwksJson() {
        return NotaryUtils.getJwks().toString();
    }

    @Override
    public boolean applyGlobally() {
        return false;
    }

    @Override
    public String getName() {
        return NAME;
    }

    @SneakyThrows
    @Override
    public Response transform(Response response, ServeEvent serveEvent) {
        var request = serveEvent.getRequest();
        var url = request.getAbsoluteUrl();
        log.info("Transformer '{}': Request - {} {}", this.hashCode(), request.getMethod(), url);

        return switch (request.getMethod().getName()) {
            case "POST" -> sign(request, response);
            default -> throw new BadRequestException("Unexpected HTTP Method");
        };
    }

    @SneakyThrows
    private Response sign(Request request, Response response) {
        if (request.getBodyAsString().contains("secretPaths")) {
            // The request is secretPath request. Issue a secrets token
            var assertion = jsonMapper.readValue(request.getBody(), SignSecretPathsRequest.class).data();
            var namespace = assertion.namespace();
            var secretPaths = assertion.secretPaths();
            log.info("Namespace '{}'. Secret paths '{}'", namespace, secretPaths);
            var payload = new SignResponse(generateJwtForSecretsAssertion(notaryBaseUrl,
                                                                          notaryClientId,
                                                                          namespace,
                                                                          secretPaths,
                                                                          jsonMapper));
            var serialized = jsonMapper.writeValueAsString(payload);
            return Response.Builder.like(response).body(serialized).build();
        }

        // Otherwise the request is worker access request. Issue a worker token
        var assertion = jsonMapper.readValue(request.getBody(), SignWorkerAccessRequest.class).data();
        var taskId = assertion.taskId();
        var ncaId = assertion.ncaId();
        log.info("NCA id '{}', Task id '{}': assertion token", ncaId, taskId);
        var payload = new SignResponse(generateJwtForWorkerAssertion(notaryBaseUrl,
                                                                     notaryClientId,
                                                                     ncaId,
                                                                     taskId,
                                                                     jsonMapper));
        var serialized = jsonMapper.writeValueAsString(payload);
        return Response.Builder.like(response).body(serialized).build();
    }

    @SneakyThrows
    private static String generateJwtForWorkerAssertion(String notaryBaseUrl,
                                                        String notaryClientId,
                                                        String ncaId,
                                                        UUID taskId,
                                                        JsonMapper jsonMapper) {
        var assertion = new WorkerAccessAssertion(ncaId, taskId);
        return NotaryUtils.getJwt(notaryClientId,
                                  jsonMapper.writeValueAsString(assertion),
                                  URI.create(notaryBaseUrl).toURL());
    }

    @SneakyThrows
    public static String generateSignedWorkerAssertion(String issuer,
                                                       String subject,
                                                       String ncaId,
                                                       UUID taskId,
                                                       Instant issuedAt,
                                                       JsonMapper jsonMapper) {
        var assertion = new WorkerAccessAssertion(ncaId, taskId);
        return NotaryUtils.getJwt(subject,
                jsonMapper.writeValueAsString(assertion),
                new URL(issuer),
                Date.from(issuedAt));
    }

    @SneakyThrows
    private static String generateJwtForSecretsAssertion(String notaryBaseUrl,
                                                         String notaryClientId,
                                                         String namespace,
                                                         List<String> secretPaths,
                                                         JsonMapper jsonMapper) {
        var assertion = new SecretPathsAssertion(namespace, secretPaths);
        return NotaryUtils.getJwt(notaryClientId,
                      jsonMapper.writeValueAsString(assertion),
                      new URL(notaryBaseUrl));
    }

    private static class NotaryUtils {

        private static final JWSSigner signer;

        @Getter
        private static final JWKSet jwks;

        static {
            try {
                var gen = KeyPairGenerator.getInstance("EC");
                gen.initialize(Curve.P_256.toECParameterSpec());
                var keyPair = gen.generateKeyPair();

                // Convert to JWK format
                var privateJwk = new ECKey.Builder(Curve.P_256,
                                                   (ECPublicKey) keyPair.getPublic())
                        .privateKey((ECPrivateKey) keyPair.getPrivate())
                        .keyID(UUID.randomUUID().toString())
                        .algorithm(JWSAlgorithm.ES256)
                        .build();
                var publicJWK = privateJwk.toPublicJWK();
                jwks = new JWKSet(publicJWK);
                signer = new ECDSASigner(privateJwk);
            } catch (Exception e) {
                throw new RuntimeException(e);
            }
        }

        @SneakyThrows(JOSEException.class)
        private static String getJwt(JWTClaimsSet.Builder claims) {
            var jwk = jwks.getKeys().get(0);
            SignedJWT signedJWT = new SignedJWT(
                    new JWSHeader.Builder((JWSAlgorithm) jwk.getAlgorithm())
                            .keyID(jwk.getKeyID())
                            .build(),
                    claims.build());
            signedJWT.sign(signer);
            return signedJWT.serialize();
        }

        @SneakyThrows
        private static String getJwt(
                String subject, String assertion, URL issuer) {
            return getJwt(subject, assertion, issuer, new Date());
        }

        @SneakyThrows
        private static String getJwt(
                String subject, String assertion, URL issuer, Date issuedAt) {
            var parser = new JSONParser(DEFAULT_PERMISSIVE_MODE);
            var jsonAssertion = parser.parse(assertion);
            var claimsSetBuilder = new JWTClaimsSet.Builder()
                    .subject(subject)
                    .issueTime(issuedAt)
                    .claim("assertion", jsonAssertion)
                    .audience(List.of(getServiceId(issuer), subject))
                    .issuer(issuer.toString())
                    .jwtID(UUID.randomUUID().toString());
            return getJwt(claimsSetBuilder);
        }
    }

}
