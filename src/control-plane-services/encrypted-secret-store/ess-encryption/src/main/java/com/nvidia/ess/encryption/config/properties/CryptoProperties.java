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
package com.nvidia.ess.encryption.config.properties;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.google.gson.JsonParseException;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import jakarta.annotation.PostConstruct;
import java.text.ParseException;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import lombok.Getter;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.boot.context.properties.source.InvalidConfigurationPropertyValueException;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.event.EventListener;


/**
 * Spring ConfigurationProperties class to load master key
 */
@RefreshScope
@Configuration
@ConfigurationProperties("kv.crypto")
@Slf4j
public class CryptoProperties extends AbstractCryptoProperties {
    @Getter
    @Setter
    private String masterKey;

    @Getter
    @Setter
    private String allMasterKeys;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    private static final String REDACTED = "redacted";

    // Populate keys from the Vault for encryption/decryption.
    // When keys are rotated in the Vault, vault agent detects the change in
    // the keys and a refresh is triggered. With RefreshScope annotation, this
    // bean gets re-initialized and init PostConstruct function triggers
    // populateKeys to reload the latest keys.
    @PostConstruct
    public void init() throws ParseException {
        log.info("reloading master encryption key");

        try {
            this.parsedMasterKey = parseKey(this.masterKey);
            this.parsedAllMasterKeys = parseAllKeys(this.allMasterKeys);

        } catch (ParseException | JsonParseException e) {
            log.error("Failed to populate encryption/decryption keys", e);
            throw e;
        }


        var currentMasterKeyEntry = parsedAllMasterKeys.get(this.parsedMasterKey.getKeyID());
        if (currentMasterKeyEntry == null || !this.parsedMasterKey.getKeyValue()
                .equals(currentMasterKeyEntry.getKeyValue())) {
            log.error("Current MEK {} is not in the list of all MEKs or has mismatching value",
                    this.parsedMasterKey.getKeyID());
            throw new InvalidConfigurationPropertyValueException("kv.crypt.masterKey", REDACTED,
                    String.format(
                            "Current MEK %s is not in the list of all MEKs or has mismatching value",
                            this.parsedMasterKey.getKeyID()));
        }

        int incompatiblePreviousKids = 0;
        for (String kid : this.parsedAllMasterKeys.keySet()) {
            try {
                UUID uuid = UUID.fromString(kid);
                if (uuid.version() != 1) {
                    if (kid.equals(this.parsedMasterKey.getKeyID())) {
                        log.error("Current MEK {} is not a UUIDv1", kid);
                        throw new InvalidConfigurationPropertyValueException("kv.crypto.masterKey",
                                REDACTED, String.format("Current MEK %s is not UUIDv1", kid));
                    } else {
                        incompatiblePreviousKids++;
                        log.warn(
                                "Previous MEK {} is not a UUIDv1. Allowed for backwards compatibility",
                                kid);
                    }
                }
            } catch (IllegalArgumentException _) {
                if (kid.equals(this.parsedMasterKey.getKeyID())) {
                    log.error("Current MEK {} is not a UUIDv1", kid);
                    throw new InvalidConfigurationPropertyValueException("kv.crypto.masterKey",
                            REDACTED, String.format("Current MEK %s is not UUIDv1", kid));
                } else {
                    incompatiblePreviousKids++;
                    log.warn(
                            "Previous MEK {} is not a UUIDv1. Allowed for backwards compatibility",
                            kid);
                }
            }
        }

        encryptionMetricsRegistry.registerMekRotationDelta(this.parsedMasterKey);
        encryptionMetricsRegistry.registerPreviousMekNotUuidV1Count(incompatiblePreviousKids);
    }

    @Getter
    private Map<String, OctetSequenceKey> parsedAllMasterKeys;
    private OctetSequenceKey parsedMasterKey;


    // use when need to access the actual loaded current MEK
    public OctetSequenceKey getActualParsedMasterKey() {
        return parsedMasterKey;
    }

    // accounts for grace period after MEK rotation
    public OctetSequenceKey getValidMek() {
        // kid was verified to be UUIDv1 already, not revalidating again
        String currentKid = parsedMasterKey.getKeyID();
        Instant now = Instant.now();
        Instant mekTimestamp = Instant.ofEpochMilli(Uuids.unixTimestamp((UUID.fromString(currentKid))));
        if (Duration.between(mekTimestamp, now)
                .compareTo(encryptionProperties.getMekRotationGracePeriod()) >= 0) {
            return parsedMasterKey;
        }


        String previousKid = null;
        // LinkedHashMap guarantees ordering
        for (String kid: parsedAllMasterKeys.keySet()) {
            // guaranteed to find it since it is validated in CryptoProperties on startup
            if (kid.equals(currentKid)) {
                break;
            }
            previousKid = kid;
        }

        // if there is no kid before the current one, then use the current one
        // This happens only if MEK was never rotated before
        if (previousKid == null) {
            return parsedMasterKey;
        }

        return parsedAllMasterKeys.get(previousKid);
    }

    /*
     * This is a workaround to disable the Lazy bean creation for @RefreshScope beans
     *
     * Normally, RefreshScope ends up destroying (can be checked by adding @PreDestroy) the bean, but the recreation is done lazily ONLY when the bean is accessed
     *
     * To avoid lazy initialization, method annotated with @EventListener is used when RefreshScopeRefreshedEvent is published.
     * EventListenerMethodProcessor bean must process an actual instance of the bean and call the annotated method,
     *  so Spring needs to eagerly create and register a bean instance (since it was destroyed already) in the Spring context, thus triggering @PostConstruct as well
     */
    @EventListener
    public void onRefreshScopeRefreshed(final RefreshScopeRefreshedEvent event) {
        log.info("Received RefreshScope Refresh event");
    }
}
