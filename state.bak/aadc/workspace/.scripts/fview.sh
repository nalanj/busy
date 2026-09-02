#!/bin/bash
# Quick file viewer with basic info
# Usage: fview <filename>

if [ -z "$1" ]; then
    echo "Usage: fview <filename>"
    exit 1
fi

echo "File: $1"
echo "Size: $(wc -c < "$1") bytes"
echo "Lines: $(wc -l < "$1")"
echo "---"
head -50 "$1"
