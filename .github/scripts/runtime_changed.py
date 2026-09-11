#!/usr/bin/env python3
"""Fail closed: only known documentation paths may skip application builds."""
import os
import subprocess


def documentation(path):
    # A markdown file outside these documentation locations is not presumed inert.
    return (path.endswith('.md') and ('/' not in path or path.startswith('docs/'))) or path == 'LICENSE'


def main():
    base = os.environ.get('BASE_SHA', '')
    head = os.environ.get('HEAD_SHA', 'HEAD')
    if not base or set(base) == {'0'}:
        print('true')
        return
    result = subprocess.run(['git', 'diff', '--no-renames', '--name-only', '-z', base, head], capture_output=True, check=True)
    paths = [p for p in result.stdout.decode().split('\0') if p]
    print('true' if any(not documentation(p) for p in paths) else 'false')


if __name__ == '__main__':
    main()
