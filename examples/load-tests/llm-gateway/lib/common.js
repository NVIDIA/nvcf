// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { Counter } from 'k6/metrics'

// Label only. Set it to whatever identifies the target, for example a region
// name, so results from separate runs stay attributable. It does not select an
// endpoint; LLM_GATEWAY_URL does that.
export const region = __ENV.LLM_REGION || 'default'

export const httpErrors = new Counter('http_errors')

export function baseUrl() {
  if (!__ENV.LLM_GATEWAY_URL) {
    throw new Error('LLM_GATEWAY_URL is required, for example https://gateway.example.com')
  }
  const url = __ENV.LLM_GATEWAY_URL.replace(/\/$/, '')
  // params() attaches TOKEN as a bearer header, so plaintext would put a
  // credential on the wire.
  if (!url.startsWith('https://')) {
    throw new Error(`LLM_GATEWAY_URL must use https, got "${url}"`)
  }
  return url
}

// Needed when addressing an ingress directly, because it serves a certificate
// for the published gateway name rather than the address you dialed.
export function skipTlsVerify() {
  return String(__ENV.LLM_INSECURE_SKIP_TLS_VERIFY || '').toLowerCase() === 'true'
}

export function modelId() {
  const functionId = __ENV.LLM_FUNCTION_ID
  const modelName = __ENV.LLM_MODEL_NAME
  if (!functionId || !modelName) {
    throw new Error('LLM_FUNCTION_ID and LLM_MODEL_NAME are required')
  }
  return `${functionId}/${modelName}`
}

// k6 defaults to a 60s request timeout. Under a ramp that turns saturation into
// recorded request failures, which reads as breakage rather than queueing.
export const requestTimeoutMs = Number(__ENV.LLM_REQUEST_TIMEOUT_MS || 300000)

export function params(endpoint, extraHeaders) {
  return {
    timeout: requestTimeoutMs,
    headers: Object.assign(
      {
        'Authorization': `Bearer ${__ENV.TOKEN}`,
        'Content-Type': 'application/json',
      },
      extraHeaders || {}
    ),
    tags: { endpoint: endpoint, region: region },
  }
}

// Records every non-200 by status code. Deliberately generic: the interesting
// failure changes over time, so let the codes speak rather than special-casing.
export function classify(response, endpoint) {
  if (response.status === 200) {
    return
  }
  httpErrors.add(1, {
    endpoint: endpoint,
    region: region,
    status: String(response.status),
  })
}

export const thresholds = {
  // Catches responses that are not failures at the HTTP layer but are still
  // wrong, for example a 200 carrying a malformed body.
  'checks': ['rate>0.99'],
  'http_req_failed': ['rate<0.01'],
  'http_req_duration{expected_response:true}': ['p(95)<5000'],
}
