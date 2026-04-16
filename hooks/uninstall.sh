#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."
rm -rf bin/
echo "✓ tempo provider cleaned up"
