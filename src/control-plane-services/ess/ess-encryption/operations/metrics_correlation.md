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
# Custom metrics observability correlation

The table below shows only the metrics emitted when "errors" (recoverable or not) metrics are emitted.

| Metric                                                                    | Tracing span attribute                                                 | Result                                                 | Description                                                                                                           |
|---------------------------------------------------------------------------|------------------------------------------------------------------------|--------------------------------------------------------|-----------------------------------------------------------------------------------------------------------------------|
| nek_rotation_errors_total{ek_namespace=?}                                 | ek.namespace=?                                                         | Rotation scheduled task succeeds (partial success)     | Rotation attempt for a specific `namespace` failed                                                                    |
| nek_reencryption_errors_total{ek_namespace=?, kid=?, encrypted_at=?}      | ek.namespace=?, ek.kid=?, ek.encrypted_at=?                            | Reencryption scheduled task succeeds (partial success) | Reencryption attempt for a NEK failed                                                                                 |
| nek_promotion_errors_total{ek_namespace=?, kid=?, encrypted_at=?}         | ek.namespace=?, ek.kid=?, ek.encrypted_at=?                            | Promotion scheduled task succeeds (partial success)    | Promotion attempt for a NEK  failed                                                                                   |
| nek_validation_errors{ek_namespace=?, kid=?, encrypted_at=?, error_key=?} | ek.namespace=?, ek.kid=?, ek.encrypted_at=?, ek.validation.error_key=? | One of NEKs failed validation                          | Look at the parent trace that contains the attribute. If not coming from NEK Promotion, then data corruption happened |
