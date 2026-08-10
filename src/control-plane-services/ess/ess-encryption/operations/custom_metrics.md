<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->
# List of custom metrics by ESS Encryption
| Metric name                         | Type    | Additional dimensions                  | Description                                                                                                                                                                                                                                                                                         |
|-------------------------------------|---------|----------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| rotation_mek_delta                  | gauge   | kid                                    | Time delta (millis) since MEK was rotated, tracked by `kid`. This should be used to alert if MEK is not rotated at expected cadence                                                                                                                                                                 |
| previous_mek_not_uuidv1             | gauge   |                                        | Count of rotated MEKs that are not uuid v1                                                                                                                                                                                                                                                          |
| nek_rotation_errors_total           | counter | [optional] ek_namespace, reason        | (cronjob only) NEK Rotation errors. If some global error, only `reason` is populated. Otherwise the erroring `ek_namespace` is populated. `reason` is a high level exception name                                                                                                                 |
| nek_rotation_age_delta              | gauge   | namespace                              | Time delta (millis) since NEK (per `namespace`) was rotated. This should be used to alert if NEK is not rotated at expected cadence                                                                                                                                                                 |
| nek_rotation_age_warnings_total     | counter | namespace                              | Count of fetched NEKs that should have been rotated already, but emitted way ahead of compliance schedule (per `namespace`)                                                                                                                                                                         |
| nek_rotation_age_criticals_total    | counter | namespace                              | Count of fetched NEKs that need to be rotated within 1-2 days, meaning that nek_rotation_age_warnings_total was not acted upon (per `namespace`)                                                                                                                                                    |
| nek_reencryption_errors_total       | counter | [optional] (ek_namespace, kid), reason | (cronjob only) NEK Re-encryption errors. If some global error, only `reason` is populated. If happening on a certain NEK, then full identifier is populated as dimensions (`ek_namespace`, `kid`). `reason` is a high level exception name                                                        |
| nek_promotion_errors_total          | counter | [optional] (ek_namespace, kid), reason | (cronjob only) NEK Promotion errors. If some global error, only `reason` is populated. If happening on a certain NEK, then full identifier is populated as dimensions (`ek_namespace`, `kid`). `reason` is a high level exception name                                                            |
| nek_validation_errors_total         | counter | ek_namespace, kid, error_key           | (cronjob & API) Partial errors when a NEK version is validated with integrity check to be decryptable (additional validation on Promotion). Will not result in data loss if NEK failsafes exist. Dimensions are the full identifier (`ek_namespace`, `kid`) and a validation error code `error_key` |
| nek_encryption_get_errors_total     | counter | ek_namespace, reason                   | (cronjob & API) Hard errors for NEK(s) reads used for Secret Encryption (per `ek_namespace`). Both NEKv1 and NEKv2 reads failed (or some disabled). `reason` is a high level exception name                                                                                                         |
| nek_decryption_get_errors_total     | counter | ek_namespace, kid, reason              | (cronjob & API) Hard errors for NEK(s) reads used for Secret Decryption (per `ek_namespace` and `kid`). Both NEKv1 and NEKv2 reads failed (or some disabled). `reason` is a high level exception name                                                                                               |
| nek_v2_encryption_get_errors_total  | counter | ek_namespace, reason                   | (cronjob & API) Partial (if NEKv1 enabled) error reads of NEKv2 tables for Secret Encryption (per `ek_namespace`). If caused by validation errors, then all NEKv2 versions are not usable. `reason` is a high level exception name                                                                  |
| nek_v2_decryption_get_errors_total  | counter | ek_namespace, kid, reason              | (cronjob & API) Partial (if NEKv1 enabled) reads of NEKv2 tables for Secret Decryption (per `ek_namespace` and `kid`). If caused by validation errors, then all NEKv2 versions are not usable. `reason` is a high level exception name                                                              |
| nek_v1_encryption_gets_total        | counter | ek_namespace, success                  | (cronjob & API) Read count (`success` for success or failure) of NEKv1 tables for Secret Encryption (per `ek_namespace`). Should be removed once NEKv1 is disabled                                                                                                                                  |
| nek_v1_encryption_gets_by_kid_total | counter | ek_namespace, kid                      | (cronjob & API) Successful read count of NEKv1 tables for Secret Encryption (per `ek_namespace` and `kid`). Should be removed once NEKv1 is disabled                                                                                                                                                |
| nek_v1_decryption_gets_total        | counter | ek_namespace, kid, success             | (cronjob & API) Read count (`success` for success or failure) of NEKv1 tables for Secret Decryption (per `ek_namespace` and `kid`). Should be removed once NEKv1 is disabled                                                                                                                        |
| nek_v2_encryption_gets_total        | counter | ek_namespace, kid                      | (cronjob & API) Success read count of NEKv2 tables for Secret Encryption (per `ek_namespace` and `kid`). Should be removed once NEKv1 is disabled                                                                                                                                                   |
| nek_v2_decryption_gets_total        | counter | ek_namespace, kid                      | (cronjob & API) Success read count of NEKv2 tables for Secret Decryption (per `ek_namespace` and `kid`). Should be removed once NEKv1 is disabled                                                                                                                                                   |

Note: some metrics will exist in both the API and Worker flows. Alerting is more likely to be required with `critical` severity on API than Worker version of the same metric.

`nek_validation_errors_total` can result in

1. `nek_v2_{en|de}cryption_get_errors_total` if all NEKv2 fail validation
2. `nek_v1_{en|de}cryption_gets_total{..., success=false}` if reads failsafe to NEKv1 and NEKv1 reads fail validation
3. `nek_{en|de}cryption_get_errors_total` if all NEK candidates fail validation

`nek_v2_{en|de}cryption_get_errors_total` does not result in hard errors if NEKv1 read succeeds. Otherwise, the error will either trigger `nek_{en|de}cryption_get_errors_total` or propagate upstream and cause either 5xx (API) or `nek_{cronjob_name}_errors_total`

Any alerts coming from a cronjob can be of `warning` severity and can be communicated with the dev team during work hours. Some metrics are purely informational since they will result in 5xx on the API, so that will trigger alerts separately

In general, these are the metrics to alert on:

* `nek_{cronjob}_errors_total` - warning
* `nek_validation_errors_total` - warning. If API, check against 5xx on LB -> escalate
* `nek_v2_{en|de}cryption_get_errors_total` - warning. If API, notify dev team even if no 5XX on LB

# Cache sets

- `encryptionKeys`
- `decryptionKeys`
