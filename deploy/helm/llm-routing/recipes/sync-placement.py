#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""Sync the GGUF chart's placement check from the SGLang chart source."""
import argparse
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='Fail if the GGUF chart copy differs from the source.')
    args = parser.parse_args()
    root = Path(__file__).resolve().parent
    source = (root / 'charts/sglang/files/placement.py').read_bytes()
    target = root / 'charts/gguf-backend/files/placement.py'
    if args.check:
        if not target.exists() or target.read_bytes() != source:
            parser.exit(1, 'Stale GGUF placement copy. Run python3 recipes/sync-placement.py from llm-routing.\n')
    else:
        target.write_bytes(source)


if __name__ == '__main__':
    main()
