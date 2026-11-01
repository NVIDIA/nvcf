# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

ess {

  # This is the address of the ESS
  address = "http://host.docker.internal:3002"

  namespace = "nvcf"

  ess_agent_token_file = "./jwt.token"

  # The default lease duration of each ess secret
  default_lease_duration = "30s"

  # The fraction of the lease duration of a secret
  lease_renewal_threshold = 0.80
}

kill_signal = "SIGINT"

template {
  source = "/ess-agent/file/templates/mockoon-example.tmpl"
  destination = "/ess-agent/file/secrets/mockoon-example.json"
}

template {
  source = "/ess-agent/file/templates/test-401.tmpl"
  destination = "/ess-agent/file/secrets/test-401.json"
  stop_processing_on_client_error = true
}

template {
  source = "/ess-agent/file/templates/test-403.tmpl"
  destination = "/ess-agent/file/secrets/test-403.json"
  stop_processing_on_client_error = true
}

template {
  source = "/ess-agent/file/templates/test-404.tmpl"
  destination = "/ess-agent/file/secrets/test-404.json"
}

telemetry {
  prometheus {
    tls_disable = true
  }
}
