<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
NVCF Function status lifecycle

INACTIVE, DEGRADED, and ERROR are not serviceable statuses, no inference requests will be accepted. The rest of the
statuses including PARTIALLY_ACTIVE and DEGRADING, are serviceable.

```mermaid

flowchart TD
    INACTIVE --> |Deployment started| DEPLOYING
    INACTIVE --> |Zero Scaling| ACTIVE
    DEPLOYING --> DEP_DECIDE{Number of instances ready?}
    DEP_DECIDE --> |GTE Min| ACTIVE
    DEP_DECIDE --> |All failed| ERROR
    ACTIVE --> ACT_DECIDE{Is Min positive?}
    ACT_DECIDE --> |No - no-op| ACTIVE
    ACT_DECIDE --> |YES| ACT_DEG_DECIDE{Number of instances ready?}
    ACT_DEG_DECIDE --> |< Min| DEGRADING
    ACT_DEG_DECIDE --> |== 0| DEGRADED
    DEGRADING --> DEGRADING_DECIDE{Number of instances ready?}
    DEGRADING_DECIDE --> |GTE Min| ACTIVE
    DEGRADING_DECIDE --> |== 0| DEGRADED
    DEGRADED --> DEGRADED_DECIDE{Number of instances ready?}
    DEGRADED_DECIDE --> |GTE Min| ACTIVE
    DEGRADED_DECIDE --> |< Min| DEGRADING

```