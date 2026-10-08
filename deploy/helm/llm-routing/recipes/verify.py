#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Verify models through the existing Helm-installed gateway and caller key."""
import argparse
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
import llm


def verify(client, models):
    if not models or any(not isinstance(model, str) or not model.strip() for model in models) or len(set(models)) != len(models):
        raise ValueError('Provide distinct, nonempty model IDs.')
    results = []
    for model in models:
        results.append({'model': model, 'discovery': client.discovery(model),
                        'chat': client.completion(model, 'Say hello in one short sentence.'),
                        'stream': client.completion(model, 'Say hello in one short sentence.', stream=True),
                        'auth': client.auth(model)})
    return {'passed': True, 'models': results}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--context')
    parser.add_argument('--namespace', default='llm-stack')
    parser.add_argument('--ca-configmap')
    parser.add_argument('--api-key-file', type=pathlib.Path)
    parser.add_argument('--model', action='append', required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    args = parser.parse_args(argv)
    if args.output.exists():
        parser.error('Use a new evidence output file.')
    if any(not model.strip() for model in args.model) or len(set(args.model)) != len(args.model):
        parser.error('Provide distinct, nonempty model IDs.')
    context = llm.selected_context(args.context)
    with llm.gateway(context, args.namespace, args.ca_configmap, args.api_key_file) as client:
        result = verify(client, args.model)
    with args.output.open('x') as out:
        args.output.chmod(0o600)
        json.dump(result, out, indent=2)
        out.write('\n')
    print('PASS: discovery, registry, chat, streaming and authentication for ' + ', '.join(args.model))


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, ValueError, OSError, KeyError, IndexError) as error:
        raise SystemExit('Verification failed: ' + str(error)) from None
