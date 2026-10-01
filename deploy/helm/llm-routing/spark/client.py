#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Chat and validate direct or authenticated gateway responses without logging keys."""
import argparse
import http.client
import json
import pathlib
import re
import ssl
import time
import urllib.parse


class Client:
    def __init__(self, url, ca_file=None, api_key_file=None):
        self.url = urllib.parse.urlsplit(url)
        if self.url.scheme not in ('http', 'https') or self.url.path not in ('', '/'):
            raise ValueError('Use an http(s) origin without a path.')
        self.context = ssl.create_default_context(cafile=ca_file) if self.url.scheme == 'https' else None
        self.key = pathlib.Path(api_key_file).read_text().strip() if api_key_file else None
        if self.key and self.url.scheme != 'https':
            raise ValueError('Authenticated requests require verified HTTPS.')

    def connect(self):
        if self.context:
            return http.client.HTTPSConnection(self.url.hostname, self.url.port or 443, context=self.context, timeout=300)
        return http.client.HTTPConnection(self.url.hostname, self.url.port or 80, timeout=300)

    def request(self, path, payload, key='configured'):
        connection = self.connect()
        headers = {'Content-Type': 'application/json'}
        token = self.key if key == 'configured' else key
        if token:
            headers['Authorization'] = 'Bearer ' + token
        connection.request('POST', path, json.dumps(payload), headers)
        return connection, connection.getresponse()

    def completion(self, model, prompt, stream=False, display=False):
        payload = {'model': model, 'messages': [{'role': 'user', 'content': prompt}],
                   'max_tokens': 512, 'temperature': 0, 'seed': 7, 'stream': stream,
                   'reasoning_effort': 'low', 'chat_template_kwargs': {'clear_thinking': True}}
        if model == 'test-model':
            payload.pop('reasoning_effort')
            payload.pop('chat_template_kwargs')
            payload['max_tokens'] = 16
        elif 'Qwen' in model:
            payload.pop('reasoning_effort')
            payload['chat_template_kwargs'] = {'enable_thinking': False}
            payload['max_tokens'] = 64
        if stream:
            payload['stream_options'] = {'include_usage': True}
        started = time.monotonic()
        connection, response = self.request('/v1/chat/completions', payload)
        record = {'model': model, 'status': response.status, 'stream': stream}
        try:
            if connection.sock and self.context:
                record['tlsVersion'] = connection.sock.version()
            if response.status != 200:
                raise RuntimeError('Chat failed with HTTP ' + str(response.status))
            if stream:
                content, reasoning, events, usage, finishes = [], [], [], [], []
                done = False
                for line in response:
                    text = line.decode().strip()
                    if not text.startswith('data:'):
                        continue
                    event = text[5:].strip()
                    if event == '[DONE]':
                        done = True
                        break
                    data = json.loads(event)
                    events.append(time.monotonic() - started)
                    if data.get('usage'):
                        usage.append(data['usage'])
                    for choice in data.get('choices', []):
                        delta = choice.get('delta', {})
                        value = delta.get('content') or ''
                        thought = delta.get('reasoning_content') or delta.get('reasoning') or ''
                        content.append(value)
                        reasoning.append(thought)
                        if value or thought:
                            record.setdefault('firstOutputSeconds', time.monotonic() - started)
                        if display and value:
                            print(value, end='', flush=True)
                        if choice.get('finish_reason'):
                            finishes.append(choice['finish_reason'])
                record.update(content=''.join(content), reasoningCharacters=len(''.join(reasoning)),
                              events=len(events), done=done, usage=usage, finishes=finishes)
                if not done or len(events) < 2 or not usage or 'stop' not in finishes:
                    raise RuntimeError('Incomplete SSE content, finish reason, usage, or completion marker.')
                if display:
                    print()
            else:
                data = json.loads(response.read())
                if data['model'] != model or data['choices'][0]['finish_reason'] != 'stop':
                    raise RuntimeError('Unexpected model or incomplete generation.')
                record.update(content=data['choices'][0]['message'].get('content') or '', usage=data['usage'])
                if display:
                    print(record['content'])
            if not record['content'].strip():
                raise RuntimeError('No final answer.')
            if model != 'test-model' and record['content'].strip() == 'xxxx':
                raise RuntimeError('Received a fixture response for a real model.')
            record['seconds'] = time.monotonic() - started
            return record
        finally:
            connection.close()

    def auth(self, model):
        if not self.key or not self.context:
            raise ValueError('Auth checks require a valid API-key file and verified HTTPS.')
        results = []
        for key, path, expected in [(None, '/v1/chat/completions', 401),
                                    ('invalid-validation-key', '/v1/chat/completions', 401),
                                    ('configured', '/v1/embeddings', 403)]:
            connection, response = self.request(path, {'model': model, 'messages': [{'role': 'user', 'content': 'Hello'}]}, key)
            response.read()
            connection.close()
            results.append({'path': path, 'status': response.status, 'expected': expected})
            if response.status != expected:
                raise RuntimeError('Unexpected auth result: ' + str(results[-1]))
        return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default='https://127.0.0.1:18443')
    parser.add_argument('--ca-file')
    parser.add_argument('--api-key-file')
    parser.add_argument('--model', default='GLM-5.3-UD-IQ2_M')
    parser.add_argument('--mode', choices=['chat', 'verify', 'auth'], default='chat')
    parser.add_argument('--retained-model', action='append', default=[])
    parser.add_argument('--stream', action='store_true')
    parser.add_argument('--output', type=pathlib.Path)
    parser.add_argument('prompt', nargs='?', default='What is 17 multiplied by 19? Give one short sentence.')
    args = parser.parse_args()
    client = Client(args.url, args.ca_file, args.api_key_file)
    report = {'result': 'FAIL', 'requests': []}
    try:
        if args.mode == 'chat':
            report['requests'].append(client.completion(args.model, args.prompt, args.stream, display=True))
        elif args.mode == 'auth':
            report['auth'] = client.auth(args.model)
        else:
            cases = [('What is 17 multiplied by 19? Give one short sentence.', lambda s: bool(re.search(r'\b323\b', s))),
                     ('Sort these numbers: 9, 2, 14, 5. Reply with only the ordered list.', lambda s: re.findall(r'\d+', s) == ['2', '5', '9', '14']),
                     ('A red box contains blue marbles. What color is the box? Reply with one word.', lambda s: bool(re.search(r'\bred\b', s, re.I)))]
            for prompt, valid in cases:
                result = client.completion(args.model, prompt)
                report['requests'].append(result)
                if not valid(result['content']):
                    raise RuntimeError('Incorrect answer: ' + result['content'])
            report['requests'].append(client.completion(args.model, 'Explain what a GPU does in three short sentences.', stream=True))
            if client.key:
                report['auth'] = client.auth(args.model)
            for model in args.retained_model:
                result = client.completion(model, 'What is 2 plus 3? Reply with only the number.')
                report['requests'].append(result)
                if model == 'test-model':
                    assert result['content'].strip() == 'xxxx'
                else:
                    assert re.search(r'\b5\b', result['content'])
        report['result'] = 'PASS'
    finally:
        if args.output:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
