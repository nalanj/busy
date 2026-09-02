#!/bin/bash
# Search for files containing text
# Usage: fsearch <pattern> [path]

PATTERN="${1:-}"
PATH="${2:-.}"

if [ -z "$PATTERN" ]; then
    echo "Usage: fsearch <pattern> [path]"
    exit 1
fi

grep -rn --color=auto "$PATTERN" "$PATH"
