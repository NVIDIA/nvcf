#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Verify several models through one existing authenticated gateway."""
import argparse
import io
import json
import pathlib
import ssl
import urllib.error
import urllib.parse
import urllib.request

MAX_BYTES = 2 * 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError('Gateway verification does not follow redirects with credentials')


def read_bounded(response):
    data = response.read(MAX_BYTES + 1)
    if len(data) > MAX_BYTES:
        raise ValueError('Gateway response exceeded the verification limit')
    return data


def parse_stream(data, model):
    text, usage, done = [], None, False
    for line in io.BytesIO(data):
        line = line.decode('utf-8').strip()
        if not line.startswith('data:'):
            continue
        event = line[5:].strip()
        if event == '[DONE]':
            done = True
            break
        item = json.loads(event)
        if 'error' in item:
            raise ValueError('Gateway returned a streaming error')
        if item.get('model') != model:
            raise ValueError('Streaming response model differs from the requested model')
        if item.get('usage'):
            usage = item['usage']
        for choice in item.get('choices', []):
            text.append(choice.get('delta', {}).get('content') or '')
    if not done or not ''.join(text).strip():
        raise ValueError('Stream did not return content and a terminal DONE event')
    if not usage or usage.get('completion_tokens', 0) <= 0:
        raise ValueError('Stream did not report completion token usage')
    return {'reply': ''.join(text), 'usage': usage}


def verify(url, ca, key, models):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != 'https' or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError('Use a verified HTTPS gateway URL without credentials, query or fragment')
    if not key.strip() or not models or len(set(models)) != len(models):
        raise ValueError('Provide a caller key and distinct model IDs')
    context = ssl.create_default_context(cafile=str(ca) if ca else None)
    opener = urllib.request.build_opener(NoRedirect(), urllib.request.HTTPSHandler(context=context))
    base = url.rstrip('/')
    def get(path):
        with opener.open(base + path, timeout=180) as response:
            return json.loads(read_bounded(response))
    discovered = {m['id'] for m in get('/models')['data']}
    registry = {m['model']: m for m in get('/registry')['models']}
    for model in models:
        if model not in discovered or registry.get(model, {}).get('health') != 'Healthy':
            raise ValueError('Model is not healthy in discovery and registry: ' + model)
    def request(model, stream, token):
        body = {'model': model, 'messages': [{'role': 'user', 'content': 'Say hello in one short sentence.'}],
                'max_tokens': 128, 'stream': stream, 'chat_template_kwargs': {'enable_thinking': False}}
        if stream:
            body['stream_options'] = {'include_usage': True}
        req = urllib.request.Request(base + '/chat/completions', data=json.dumps(body).encode(),
                                     headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
        with opener.open(req, timeout=180) as response:
            return read_bounded(response)
    results = []
    for model in models:
        response = json.loads(request(model, False, key))
        if response.get('model') != model or not response.get('choices', [{}])[0].get('message', {}).get('content'):
            raise ValueError('Chat response has wrong model or no content: ' + model)
        if response.get('usage', {}).get('completion_tokens', 0) <= 0:
            raise ValueError('Chat response lacks completion token usage: ' + model)
        stream = parse_stream(request(model, True, key), model)
        results.append({'model': model, 'chat': response['choices'][0]['message']['content'],
                        'usage': response['usage'], 'stream': stream})
    try:
        request(models[0], False, 'deliberately-invalid-recipe-test-key')
    except urllib.error.HTTPError as e:
        if e.code != 401:
            raise ValueError('Invalid key did not return HTTP 401') from e
    else:
        raise ValueError('Gateway accepted an invalid API key')
    return {'passed': True, 'gateway': base, 'models': results, 'invalidKeyStatus': 401}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url', required=True, help='Gateway base URL including /v1')
    p.add_argument('--ca', type=pathlib.Path)
    p.add_argument('--key-file', type=pathlib.Path, required=True)
    p.add_argument('--model', action='append', required=True)
    p.add_argument('--output', type=pathlib.Path, required=True)
    a = p.parse_args()
    if a.output.exists():
        p.error('Use a new evidence output file')
    result = verify(a.url, a.ca, a.key_file.read_text().strip(), a.model)
    with a.output.open('x') as f:
        a.output.chmod(0o600)
        json.dump(result, f, indent=2)
    print('PASS: discovery, registry, chat, streaming and authentication for ' + ', '.join(a.model))


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, IndexError) as e:
        raise SystemExit('Verification failed: ' + str(e))
