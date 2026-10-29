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
package com.nvidia.icms.configuration;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.nvidia.icms.configuration.bean.CapacityGatingProperties;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Bindable;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.boot.context.properties.source.MapConfigurationPropertySource;

/**
 * Pins how {@code icms.capacity-gating} merges across property sources, which is what the rollout
 * sequence depends on.
 *
 * <p>SIS reads the packaged {@code application-{profile}.yaml} first and then overlays the
 * {@code gfn-spot} and {@code gfn-spot-{profile}} ConfigMaps on top. For a map, the binder unions
 * keys across sources and lets the highest-precedence source win per key. That is why shipping a
 * policy in the jar is safe during the rollout, and equally why removing one later is an image
 * change: a ConfigMap can add or replace an entry but has no way to delete one the jar declares.
 */
class CapacityGatingBindingTest {

    private static final String PREFIX = "icms.capacity-gating.";

    private static final String JAR_NCA_ID = "jar-nca";
    private static final String CONFIGMAP_NCA_ID = "configmap-nca";

    /** Binds with {@code configMap} taking precedence over {@code jar}, as the runtime does. */
    private static CapacityGatingProperties bind(Map<String, Object> jar,
                                                    Map<String, Object> configMap) {
        Binder binder = new Binder(List.of(new MapConfigurationPropertySource(configMap),
                                           new MapConfigurationPropertySource(jar)));
        return binder.bind("icms", Bindable.of(CapacityGatingProperties.class))
                .orElseGet(CapacityGatingProperties::new);
    }

    private static Map<String, Object> gpuPolicy(String ncaId, String gpu,
                                                 String... instanceTypes) {
        Map<String, Object> properties = new LinkedHashMap<>();
        for (int i = 0; i < instanceTypes.length; i++) {
            properties.put(PREFIX + ncaId + ".gpus." + gpu + "[" + i + "]", instanceTypes[i]);
        }
        return properties;
    }

    /**
     * Both entries survive: the ConfigMap adds its own account without displacing the one the jar
     * declares. This is the guarantee that lets a policy ship in the image while the ConfigMap
     * catches up.
     */
    @Test
    void entriesFromBothSourcesSurvive() {
        CapacityGatingProperties props = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"),
                gpuPolicy(CONFIGMAP_NCA_ID, "B40", "gb40_1.br30_2xlarge"));

        assertEquals(Set.of(JAR_NCA_ID, CONFIGMAP_NCA_ID), props.getCapacityGating().keySet());
        assertEquals(Map.of("L40S", List.of("gl40s_1.br25_2xlarge")),
                     props.getCapacityGating().get(JAR_NCA_ID).getGpus());
        assertEquals(Map.of("B40", List.of("gb40_1.br30_2xlarge")),
                     props.getCapacityGating().get(CONFIGMAP_NCA_ID).getGpus());

        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"));
        assertTrue(props.isInstanceTypeAllowedForNca(CONFIGMAP_NCA_ID, "B40",
                                                     "gb40_1.br30_2xlarge"));
    }

    /**
     * Within one account the higher-precedence list replaces rather than appends, so a ConfigMap
     * can narrow or widen a policy the jar shipped.
     */
    @Test
    void configMapReplacesTheJarListForTheSameAccount() {
        CapacityGatingProperties props = bind(
                gpuPolicy(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge", "gb40_2.br30_large"),
                gpuPolicy(JAR_NCA_ID, "B40", "gb40_4.br30_small"));

        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_4.br30_small"));
        assertFalse(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
        assertFalse(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_2.br30_large"));
    }

    /**
     * The one-way door. A ConfigMap that declares other accounts, or declares the key empty,
     * cannot drop the jar's entry, so ungating an account shipped in the image needs a new image.
     */
    @Test
    void configMapCannotDeleteAnEntryTheJarDeclares() {
        CapacityGatingProperties withOtherAccount = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"),
                gpuPolicy(CONFIGMAP_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
        assertTrue(withOtherAccount.hasCapacityGating(JAR_NCA_ID));

        CapacityGatingProperties withEmptyOverride = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"),
                Map.of(PREFIX.substring(0, PREFIX.length() - 1), Map.of()));
        assertTrue(withEmptyOverride.hasCapacityGating(JAR_NCA_ID));
        assertTrue(withEmptyOverride.isInstanceTypeAllowedForNca(JAR_NCA_ID, "L40S",
                                                                 "gl40s_1.br25_2xlarge"));
    }

    /**
     * The compiled lookup is rebuilt by the setter the binder calls, so a bound policy is in force
     * without waiting for the {@code @PostConstruct} hook.
     */
    @Test
    void boundPolicyIsInForceImmediately() {
        CapacityGatingProperties props = bind(
                gpuPolicy(JAR_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"), Map.of());

        assertTrue(props.hasCapacityGating(JAR_NCA_ID));
        assertFalse(props.isGpuAllowedForNca(JAR_NCA_ID, "B40"));
        assertFalse(props.hasCapacityGating(CONFIGMAP_NCA_ID));
    }

    /**
     * Migrating a ConfigMap means moving the GPU map down one level under {@code gpus}. Writing
     * the old shape straight under the new key makes every property below the NCA ID unknown, and
     * because unknown properties are ignored rather than rejected the binder produces no map entry
     * at all. The account is left ungated, not denied.
     *
     * <p>That direction is deliberate to record: a half-converted entry over-grants rather than
     * locking an account out, so it cannot cause a lost-access complaint, but it also will not
     * announce itself. The only signal is the absence of the account from the policy map.
     */
    @Test
    void oldGpuGatingShapeUnderTheNewKeyLeavesTheAccountUngated() {
        Map<String, Object> source = new LinkedHashMap<>();
        source.put(PREFIX + JAR_NCA_ID + ".L40S[0]", "gl40s_1.br25_2xlarge");
        // A correctly shaped neighbour, so a failure here cannot be mistaken for the whole bean
        // failing to bind.
        source.putAll(gpuPolicy(CONFIGMAP_NCA_ID, "L40S", "gl40s_1.br25_2xlarge"));

        CapacityGatingProperties props = bind(source, Map.of());

        assertTrue(props.hasCapacityGating(CONFIGMAP_NCA_ID));

        assertFalse(props.getCapacityGating().containsKey(JAR_NCA_ID));
        assertFalse(props.hasCapacityGating(JAR_NCA_ID));
        assertFalse(props.isOrgDenied(JAR_NCA_ID));
        // Ungated means unrestricted, including GPUs the half-converted entry meant to exclude.
        assertTrue(props.isInstanceTypeAllowedForNca(JAR_NCA_ID, "B40", "gb40_1.br30_2xlarge"));
    }

    /**
     * {@code icms.gpu-allowed-nca-ids} moved onto this bean, and it is the key that restricts
     * limited GPUs in production. Bound here from a ConfigMap-shaped source so the move is proven
     * against the binder and not just against a setter.
     */
    @Test
    void gpuAllowedNcaIdsStillBindsOntoThisBean() {
        CapacityGatingProperties props = bind(Map.of(),
                Map.of("icms.gpu-allowed-nca-ids.B40[0]", "granted-nca",
                       "icms.gpu-allowed-nca-ids.B40[1]", "second-granted-nca"));

        assertEquals(Map.of("B40", List.of("granted-nca", "second-granted-nca")),
                     props.getGpuAllowedNcaIds());

        assertTrue(props.isNcaAllowedForGpu("B40", "granted-nca"));
        assertTrue(props.isNcaAllowedForGpu("B40", "second-granted-nca"));
        assertFalse(props.isNcaAllowedForGpu("B40", "other-nca"));
        // A GPU the key does not name stays unrestricted.
        assertTrue(props.isNcaAllowedForGpu("L40S", "other-nca"));
    }
}
