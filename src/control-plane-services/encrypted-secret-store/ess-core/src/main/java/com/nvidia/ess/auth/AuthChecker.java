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
package com.nvidia.ess.auth;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jwt.SignedJWT;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.boot.exceptions.UpstreamException;
import com.nvidia.ess.auth.jwt.JwtClaimValidator;
import com.nvidia.ess.auth.jwt.JwtSignatureValidationService;
import com.nvidia.ess.constants.AuthRoles;
import com.nvidia.ess.constants.Constants;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import com.nvidia.ess.exceptions.FallbackToTenantCheckException;
import com.nvidia.ess.facade.AuthorizationsFacade;
import com.nvidia.ess.filter.ReactiveRequestContextHolder;
import java.net.URI;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.stereotype.Component;
import reactor.core.publisher.Mono;

// TODO: Use AuthChecker in @Preauthorize
@Component
@Slf4j
public class AuthChecker {
    record AuthContext(JwtClaimValidator validator, AuthorizationInfo authorizationInfo) {}

    @Autowired
    private JwtSignatureValidationService jwtSignatureValidationService;

    @Autowired
    private AuthorizationsFacade authorizationsFacade;

    @Autowired
    private AuthProperties authProperties;

    @Autowired
    private OperatorProperties essOperatorProperties;

    @Autowired
    private NotaryProperties notaryProperties;

    /*************************************
     Helper methods
     *************************************/

    Mono<SignedJWT> getJwtWithoutSignatureValidation(String authHeader) {

        try {
            String tokenString = authHeader.replace("Bearer ", "");
            return Mono.just(SignedJWT.parse(tokenString));
        } catch (Exception e) {
            String errMsg = String.format("failed to parse signed jwt: %s ", e.getMessage());
            log.error(errMsg);
            return Mono.error(() -> new UnauthorizedException(errMsg, e));
        }
    }

    Mono<String> getClientID(SignedJWT jwt) {
        try {
            return Mono.just(jwt.getJWTClaimsSet().getSubject());
        } catch (Exception e) {
            String errMsg = String.format("failed to get subject from signed jwt: %s ", e.getMessage());
            log.error(errMsg);
            return Mono.error(() -> new UnauthorizedException(errMsg, e));
        }
    }

    Mono<JWSAlgorithm> getAlgorithm(SignedJWT jwt) {
        try {
            return Mono.just(jwt.getHeader().getAlgorithm());
        } catch (Exception e) {
            String errMsg = String.format("failed to get algorithm from signed jwt:  %s ", e.getMessage());
            log.error(errMsg);
            return Mono.error(() -> new UnauthorizedException(errMsg, e));
        }
    }

    Mono<String> deriveAudience(AuthorizationInfo authInfo, AuthRoles role) {

        String aud = "";
        switch (role) {
            case TENANT_NOTARY:
                // in Notary token we would have aud=<service-id>
                aud = authProperties.getServiceId();
                break;
            case OPERATOR:
                // in non-Notary token we would have aud=s:<service-id>
                aud = "s:" + authProperties.getServiceId();
                break;
            case TENANT_SERVICE:
                // extract aud from iss for TENANT_SERVICE
                try {
                    String iss = authInfo.getIss();
                    aud = URI.create(iss).getHost().split("\\.")[0];
                } catch (Exception e) {
                    return Mono.error(() ->
                            new UnauthorizedException(String.format("failed to extract aud reason: %s", e.getMessage()), e));
                }
                aud = "s:" + aud;
                break;
        }
        return Mono.just(aud);
    }

    Mono<Jwt> getJwtWithSignatureValidation(SignedJWT jwt, AuthorizationInfo authInfo) {
        return getAlgorithm(jwt)
                // feed algo for decoding the token
                .flatMap(algo -> jwtSignatureValidationService.getJwt(authInfo.getJwks(), algo, jwt.getParsedString()));
    }

    Mono<JwtClaimValidator> validateJWT(SignedJWT jwt, AuthorizationInfo authInfo,
                                        AuthRoles role) {

        return  getJwtWithSignatureValidation(jwt, authInfo)
                // decoding is successful
                // map decodedJWT --> validator
                .map(JwtClaimValidator::JwtClaimValidatorWithJwt)
                // match iss
                .flatMap(validator -> validator.validateIssuer(authInfo.getIss()))
                // match Aud
                .flatMap(validator -> deriveAudience(authInfo, role).flatMap(validator::validateAud))
                // match sub with id
                .flatMap(validator -> validator.validateSubject(authInfo.getId()));
    }

    Mono<Boolean> authorizeClient(JwtClaimValidator validator, String[] authScopes) {

        return validator.validateScopes(authScopes)
                .map(v -> true);
    }

    Mono<Boolean> authorizeClient(JwtClaimValidator validator, String namespace,
                                          String scrtPath) {

        return validator.validateAssertions(namespace, scrtPath, notaryProperties)
                .map(v -> true);

    }

    /* ONLY FOR TENANT AND NOTARY CLIENTS
     * There are following steps in authentication
     * 1. Parse the Token without signature validation. Error out if token is bad for parsing
     * 2. If parsing was successful ,then extract sub(as clientID) from it
     * 3. Look up AuthInfo in DB for JWKS/ISS using client id extracted
     * 4. Using JWKS, download public key and validate signature. Cache the public key.
     * If token contains expiration time e.g. in non-Notary token then check that.
     * If token does not contain exp. then do validation in authorization phase
     * 5. Validate ISS/Aud/Subject as expected
     */
        Mono<AuthContext> authenticateHelper(String namespace, String authHeader,
                                             boolean isNotary, AuthRoles role) {

            return
                    // get Signed JWT token handle (note signature is not validated yet)
                    getJwtWithoutSignatureValidation(authHeader)
                    // token is okay except for signature
                    .flatMap(signedJWT ->
                            // get ClientID
                            getClientID(signedJWT)
                            // Check if this is the Operator. Operator is nor allowed this operation
                            .flatMap(clientID -> {
                                if (clientID.equals(essOperatorProperties.getClientID())) {
                                    return Mono.error(new ForbiddenException(Constants.MSG_OPERATION_NOT_ALLOWED_BY_OPERATOR));
                                }
                                // look up auth info for ISS/JWKS and ID
                                return authorizationsFacade.getAuthorization(namespace, isNotary, clientID)
                                        // validate token
                                        .flatMap(authInfo ->
                                                ReactiveRequestContextHolder.getExchange()
                                                        .doOnNext(exchange -> exchange.getAttributes().put("authInfo", authInfo))
                                                        .then(validateJWT(signedJWT, authInfo, role)
                                                                .flatMap(v -> Mono.just(new AuthContext(v, authInfo))))
                                        )
                                        .onErrorResume(e -> {
                                            if (e instanceof ForbiddenException || e instanceof UnauthorizedException) {
                                                return Mono.error(e);
                                            }
                                            String errMsg = String.format(Constants.MSG_FAILED_TO_LOOKUP_AUTH + " reason: %s", e.getMessage());
                                            log.error(errMsg);
                                            if (e instanceof NotFoundException) {
                                                // if exception is "NotFoundException" send back forbidden error
                                                return Mono.error(new ForbiddenException(String.format(Constants.MSG_CLIENT_ID_NOT_REGISTERED, clientID)));
                                            }
                                            // send upstream exception error if C* is down
                                            return Mono.error(new UpstreamException(errMsg, e));
                                        });
                            }));

        }

    Mono<AuthContext> authenticateTenant(String namespace, String authHeader) {
        return authenticateHelper(namespace, authHeader, false,
                AuthRoles.TENANT_SERVICE);
    }

    Mono<AuthContext> authenticateNotary(String namespace, String authHeader) {
        return authenticateHelper(namespace, authHeader, true,
                AuthRoles.TENANT_NOTARY);
    }

    Mono<Boolean> authOperator(String authHeader, String[] essAuthScopes, boolean fallBackToTenantCheck) {
        return getJwtWithoutSignatureValidation(authHeader)
                .flatMap(signedJWT -> {
                    try {
                        if (!signedJWT.getJWTClaimsSet().getSubject().equals(
                                essOperatorProperties.getClientID())) {
                            if (fallBackToTenantCheck) {
                                return Mono.error(new FallbackToTenantCheckException("fallback to tenant check occurred"));
                            }
                            return Mono.error(new ForbiddenException(Constants.MSG_OPERATION_ALLOWED_BY_OPERATOR_ONLY));
                        }
                    } catch (Exception e) {
                        return Mono.error(new UnauthorizedException(e.getMessage(), e));
                    }

                    return Mono.just(signedJWT);
                })
                .flatMap(signedJWT -> {
                    AuthorizationInfo authInfo = AuthorizationInfo.builder()
                            .jwks(essOperatorProperties.getJwks())
                            .iss(essOperatorProperties.getIss())
                            .id(essOperatorProperties.getClientID())
                            .build();

                    // Store authInfo in the ReactiveRequestContextHolder
                    return ReactiveRequestContextHolder.getExchange()
                            .doOnNext(exchange -> exchange.getAttributes().put("authInfo", authInfo))
                            .then(validateJWT(signedJWT, authInfo, AuthRoles.OPERATOR));
                })
                .flatMap(validator -> authorizeClient(validator, essAuthScopes));
    }

    Mono<Boolean> authTenantWithSelfRemovalCheck(String namespace, String authHeader, String[] authScopes, String clientIDBeingRemoved) {

        return authenticateTenant(namespace, authHeader)
                .flatMap(authContext -> {
                    if (clientIDBeingRemoved != null &&
                            clientIDBeingRemoved.equals(authContext.authorizationInfo.getId())) {
                        return Mono.error(
                                new ForbiddenException(Constants.MSG_CAN_NOT_REMOVE_SELF));
                    }
                    return Mono.just(authContext);
                })
                .flatMap(authContext -> authorizeClient(authContext.validator, authScopes));
    }

    /**
     * Authenticates and Authorizes an operator based on the provided authorization header and required scopes.
     *
     * @param authHeader    the authorization header containing the auth token header for the operator.
     * @param essAuthScopes an array of required authorization scopes that the operator must have.
     * @return Mono<Boolean> emitting true if the operator is authorized, NotAuthorized/Forbidden exception otherwise.
     */

    public Mono<Boolean> authOperator(String authHeader, String[] essAuthScopes) {
        return authOperator(authHeader, essAuthScopes, false);
    }


    /**
     * Authenticates and Authorizes ESS tenant (non-Notary) client based on the provided authorization
     * header and required scopes.
     *
     * @param authHeader the authorization header containing the auth token header for the operator.
     * @param authScopes an array of required authorization scopes that the operator must have.
     * @return Mono<Boolean> emitting true if the operator is authorized, NotAuthorized/Forbidden exception otherwise.
     */
    public Mono<Boolean> authTenant(String namespace, String authHeader, String[] authScopes) {
        return authTenantWithSelfRemovalCheck(namespace, authHeader, authScopes, null);
    }

    /**
     * Authenticates and Authorizes a tenant operation with an additional check for self-removal.
     * Self Removal is a condition where if client is trying to remove its own non-Notary client ID
     * then it will not be allowed
     *
     * @param authHeader           the authorization header containing the auth token header for the operator.
     * @param authScopes           an array of required authorization scopes that the operator must have.
     * @param clientIDBeingRemoved client ID being removed
     * @return Mono<Boolean> emitting true if the operator is authorized, NotAuthorized/Forbidden exception otherwise.
     */
    public Mono<Boolean> authTenant(String namespace, String authHeader, String[] authScopes, String clientIDBeingRemoved) {
        return authTenantWithSelfRemovalCheck(namespace, authHeader, authScopes, clientIDBeingRemoved);
    }

    /**
     * Authenticates and Authorizes either an operator or a tenant based on the provided namespace, authorization header,
     * and required authorization scopes.
     *
     * @param namespace          The namespace of the tenant or operator.
     * @param authHeader         The authorization header containing the credentials or token for authentication.
     * @param operatorAuthScopes An array of required authorization scopes that the operator must have.
     * @param tenantAuthScopes   An array of required authorization scopes that the tenant must have.
     * @return A Mono<Boolean>emitting {true} if either the operator or the tenant is authorized, NotAuthorized/Forbidden exception otherwise. otherwise.
     */
    public Mono<Boolean> authOperatorOrTenant(String namespace, String authHeader,
                                              @NonNull String[] operatorAuthScopes,
                                              @NonNull String[] tenantAuthScopes) {

        return authOperator(authHeader, operatorAuthScopes, true)
                .onErrorResume(e -> {
                    // If AuthOperatorErrorException then continue to tenant check
                    // If other error return the error to client without further check
                    if (e instanceof FallbackToTenantCheckException) {
                        return authTenant(namespace, authHeader, tenantAuthScopes);
                    }
                    return Mono.error(e);
                });
    }

    /**
     * Authenticates and Authorizes a notary client based on the provided namespace, authorization header for secret access.
     *
     * @param namespace  The namespace of the notary client.
     * @param authHeader The authorization header containing the credentials or token for authentication.
     * @param scrtPath The path to the secret used for additional verification.
     * @return A {@link Mono} emitting {@code true} if the notary client is authorized, {@code false} otherwise.
     */
    public Mono<Boolean> authNotaryClient(String namespace, String authHeader, String scrtPath) {

        return authenticateNotary(namespace, authHeader)
                .flatMap(authContext -> authorizeClient(authContext.validator, namespace,
                        scrtPath));
    }
}