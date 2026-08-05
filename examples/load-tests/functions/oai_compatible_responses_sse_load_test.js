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

import { check } from 'k6'
import sse from 'k6/x/sse'
import { Counter, Rate, Trend } from 'k6/metrics'

const PROFILE_CALIBRATION = 'calibration'
const PROFILE_LOAD = 'load'

const firstSSEEventMs = new Trend('openai_responses_first_sse_event_ms', true)
const ttftMs = new Trend('openai_responses_ttft_ms', true)
const itlMs = new Trend('openai_responses_itl_ms', true)
const outputChunksPerSecond = new Trend('openai_responses_output_chunks_per_second')
const declaredTokensPerSecond = new Trend('openai_responses_declared_tokens_per_second')
const streamDurationMs = new Trend('openai_responses_stream_duration_ms', true)
const emittedOutputChunks = new Counter('openai_responses_output_chunks')
const declaredOutputTokens = new Counter('openai_responses_declared_output_tokens')
const streamsCompleted = new Counter('openai_responses_streams_completed')
const streamsFailed = new Counter('openai_responses_streams_failed')
const streamsTruncated = new Counter('openai_responses_streams_truncated')
const protocolErrors = new Counter('openai_responses_protocol_errors')
const expectedDeltaMismatches = new Counter('openai_responses_expected_delta_mismatches')
const streamSuccess = new Rate('openai_responses_stream_success')

const config = buildConfig()

export const options = buildOptions()

export function setup() {
    if (config.url === '') {
        throw new Error('OAI_COMPAT_URL must be the full /v1/responses endpoint URL')
    }
}

export default function () {
    const requestStartedAt = Date.now()
    let firstEventAt = null
    let firstDeltaAt = null
    let lastDeltaAt = null
    let deltaCount = 0
    let completed = false
    let protocolError = false
    let transportError = false
    let earlyITL = false

    const response = sse.open(config.url, requestParams(), function (client) {
        client.on('event', function (event) {
            const eventAt = Date.now()
            if (firstEventAt === null) {
                firstEventAt = eventAt
            }

            if (event.name === 'error') {
                protocolError = true
                client.close()
                return
            }

            if (event.name === 'response.output_text.delta') {
                const data = parseEvent(event)
                if (data === null || data.type !== event.name || typeof data.delta !== 'string') {
                    protocolError = true
                    return
                }

                if (firstDeltaAt === null) {
                    firstDeltaAt = eventAt
                } else {
                    const interval = eventAt - lastDeltaAt
                    itlMs.add(interval)
                    if (config.profile === PROFILE_CALIBRATION && interval < calibrationITLMinimum()) {
                        earlyITL = true
                    }
                }

                lastDeltaAt = eventAt
                deltaCount += 1
                return
            }

            if (event.name === 'response.completed') {
                const data = parseEvent(event)
                if (data === null || data.type !== event.name) {
                    protocolError = true
                } else {
                    completed = true
                }
                client.close()
            }
        })

        client.on('error', function () {
            transportError = true
            client.close()
        })
    })

    const streamFinishedAt = Date.now()
    const status = response && response.status ? response.status : 0
    const responseError = response && response.error ? response.error : null
    const hasTransportError = transportError || responseError !== null
    const firstEventDuration = firstEventAt === null ? null : firstEventAt - requestStartedAt
    const ttftDuration = firstDeltaAt === null ? null : firstDeltaAt - requestStartedAt
    const outputDuration = firstDeltaAt === null || lastDeltaAt === null ? 0 : lastDeltaAt - firstDeltaAt
    const expectedDeltaCount = config.expectedDeltas === 0 || deltaCount === config.expectedDeltas
    const success = status === 200 && completed && !protocolError && !hasTransportError && expectedDeltaCount

    streamDurationMs.add(streamFinishedAt - requestStartedAt)
    if (firstEventDuration !== null) {
        firstSSEEventMs.add(firstEventDuration)
    }
    if (ttftDuration !== null) {
        ttftMs.add(ttftDuration)
    }
    if (success) {
        emittedOutputChunks.add(deltaCount)
        declaredOutputTokens.add(deltaCount * config.tokensPerChunk)
    }
    if (success && deltaCount > 1 && outputDuration > 0) {
        const chunksPerSecond = (deltaCount - 1) * 1000 / outputDuration
        outputChunksPerSecond.add(chunksPerSecond)
        declaredTokensPerSecond.add(chunksPerSecond * config.tokensPerChunk)
    }

    if (completed) {
        streamsCompleted.add(1)
    }
    if (protocolError) {
        protocolErrors.add(1)
    }
    if (!completed && !protocolError && !hasTransportError && status === 200) {
        streamsTruncated.add(1)
    }
    if (!expectedDeltaCount) {
        expectedDeltaMismatches.add(1)
    }
    if (!success) {
        streamsFailed.add(1)
    }
    streamSuccess.add(success)

    const checks = {
        'responses stream returns HTTP 200': function (result) {
            return result.status === 200
        },
        'responses stream completes': function (result) {
            return result.completed
        },
        'responses stream has no protocol error': function (result) {
            return !result.protocolError
        },
        'responses stream has no transport error': function (result) {
            return !result.transportError
        },
    }
    if (config.expectedDeltas !== 0) {
        checks['responses stream emits the expected delta count'] = function (result) {
            return result.expectedDeltaCount
        }
    }
    if (config.profile === PROFILE_CALIBRATION) {
        checks['calibration first SSE event is not early'] = function (result) {
            return result.firstEventDuration !== null && result.firstEventDuration >= calibrationStartMinimum()
        }
        checks['calibration first token is not early'] = function (result) {
            return result.ttftDuration !== null && result.ttftDuration >= calibrationStartMinimum()
        }
        checks['calibration inter-token latency is not early'] = function (result) {
            return result.deltaCount > 1 && !result.earlyITL
        }
    }

    check({
        status: status,
        completed: completed,
        protocolError: protocolError,
        transportError: hasTransportError,
        expectedDeltaCount: expectedDeltaCount,
        firstEventDuration: firstEventDuration,
        ttftDuration: ttftDuration,
        deltaCount: deltaCount,
        earlyITL: earlyITL,
    }, checks)
}

function buildConfig() {
    const profile = stringEnv('OPENAI_RESPONSES_PROFILE', PROFILE_CALIBRATION).toLowerCase()
    if (profile !== PROFILE_CALIBRATION && profile !== PROFILE_LOAD) {
        throw new Error('OPENAI_RESPONSES_PROFILE must be calibration or load')
    }

    const isCalibration = profile === PROFILE_CALIBRATION
    const outputChunks = integerEnv('LOAD_TESTER_OUTPUT_CHUNKS', 8, 1)
    const tokensPerChunk = integerEnv('OPENAI_RESPONSES_TOKENS_PER_CHUNK', 1, 1)
    const chunk = stringEnv('LOAD_TESTER_CHUNK', 'xxxx')
    if (chunk === '') {
        throw new Error('LOAD_TESTER_CHUNK must not be empty')
    }

    return {
        profile: profile,
        url: stringEnv('OAI_COMPAT_URL', ''),
        token: stringEnv('TOKEN', ''),
        model: stringEnv('LLM_MODEL_NAME', 'test-model'),
        input: stringEnv('OPENAI_RESPONSES_INPUT', 'benchmark'),
        timeout: stringEnv('OPENAI_RESPONSES_TIMEOUT', '60s'),
        vus: integerEnv('OPENAI_RESPONSES_VUS', isCalibration ? 1 : 10, 1),
        iterations: integerEnv('OPENAI_RESPONSES_ITERATIONS', 10, 1),
        calibrationMaxDuration: stringEnv('OPENAI_RESPONSES_MAX_DURATION', '10m'),
        duration: stringEnv('OPENAI_RESPONSES_DURATION', '30s'),
        expectedDeltas: integerEnv('OPENAI_RESPONSES_EXPECTED_DELTAS', isCalibration ? outputChunks : 0, 0),
        tokensPerChunk: tokensPerChunk,
        outputChunks: outputChunks,
        chunk: chunk,
        queueDelayMs: optionalIntegerEnv('LOAD_TESTER_QUEUE_DELAY_MS', 0, isCalibration),
        ttftDelayMs: optionalIntegerEnv('LOAD_TESTER_TTFT_MS', 200, isCalibration),
        ttftJitterMs: optionalIntegerEnv('LOAD_TESTER_TTFT_JITTER_MS', 0, isCalibration),
        itlDelayMs: optionalIntegerEnv('LOAD_TESTER_ITL_MS', 50, isCalibration),
        itlJitterMs: optionalIntegerEnv('LOAD_TESTER_ITL_JITTER_MS', 0, isCalibration),
        streamErrorAfterChunks: optionalIntegerEnv('LOAD_TESTER_STREAM_ERROR_AFTER_CHUNKS', 0, false),
        streamTruncateAfterChunks: optionalIntegerEnv('LOAD_TESTER_STREAM_TRUNCATE_AFTER_CHUNKS', 0, false),
        calibrationToleranceMs: integerEnv('OPENAI_RESPONSES_CALIBRATION_TOLERANCE_MS', 10, 0),
    }
}

function buildOptions() {
    const scenarios = {}
    if (config.profile === PROFILE_CALIBRATION) {
        scenarios.calibration = {
            executor: 'per-vu-iterations',
            vus: config.vus,
            iterations: config.iterations,
            maxDuration: config.calibrationMaxDuration,
        }
    } else {
        scenarios.load = {
            executor: 'constant-vus',
            vus: config.vus,
            duration: config.duration,
        }
    }

    return {
        scenarios: scenarios,
        thresholds: {
            checks: ['rate==1'],
            openai_responses_stream_success: ['rate==1'],
        },
    }
}

function requestParams() {
    const headers = {
        'Accept': 'text/event-stream',
        'Content-Type': 'application/json',
        'X-Load-Tester-Chunk': config.chunk,
        'X-Load-Tester-Output-Chunks': String(config.outputChunks),
    }
    if (config.token !== '') {
        headers.Authorization = `Bearer ${config.token}`
    }
    addHeader(headers, 'X-Load-Tester-Queue-Delay-Ms', config.queueDelayMs)
    addHeader(headers, 'X-Load-Tester-TTFT-Ms', config.ttftDelayMs)
    addHeader(headers, 'X-Load-Tester-TTFT-Jitter-Ms', config.ttftJitterMs)
    addHeader(headers, 'X-Load-Tester-ITL-Ms', config.itlDelayMs)
    addHeader(headers, 'X-Load-Tester-ITL-Jitter-Ms', config.itlJitterMs)
    addHeader(headers, 'X-Load-Tester-Stream-Error-After-Chunks', config.streamErrorAfterChunks)
    addHeader(headers, 'X-Load-Tester-Stream-Truncate-After-Chunks', config.streamTruncateAfterChunks)

    return {
        method: 'POST',
        body: JSON.stringify({
            model: config.model,
            input: config.input,
            stream: true,
        }),
        timeout: config.timeout,
        headers: headers,
        tags: {
            name: 'OpenAIResponsesSSE',
            profile: config.profile,
        },
    }
}

function addHeader(headers, name, value) {
    if (value !== undefined) {
        headers[name] = String(value)
    }
}

function parseEvent(event) {
    try {
        return JSON.parse(event.data)
    } catch (error) {
        return null
    }
}

function calibrationStartMinimum() {
    return Math.max(0, (config.queueDelayMs || 0) + (config.ttftDelayMs || 0) - config.calibrationToleranceMs)
}

function calibrationITLMinimum() {
    return Math.max(0, (config.itlDelayMs || 0) - config.calibrationToleranceMs)
}

function stringEnv(name, defaultValue) {
    const value = __ENV[name]
    return value === undefined || value === '' ? defaultValue : value
}

function integerEnv(name, defaultValue, minimum) {
    const value = optionalIntegerEnv(name, defaultValue, true)
    if (value < minimum) {
        throw new Error(`${name} must be at least ${minimum}`)
    }
    return value
}

function optionalIntegerEnv(name, defaultValue, useDefault) {
    const raw = __ENV[name]
    if (raw === undefined || raw === '') {
        return useDefault ? defaultValue : undefined
    }
    const value = Number(raw)
    if (!isFinite(value) || Math.floor(value) !== value || value < 0) {
        throw new Error(`${name} must be a non-negative integer`)
    }
    return value
}
