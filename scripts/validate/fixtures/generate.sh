#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Slavi Pantaleev
#
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Regenerates the deterministic validation image used by the attachment tests.
#
# The image is a 64x64 PNG built only from ImageMagick primitives with `-strip`
# (no timestamps, no profiles), so repeated runs must produce byte-identical
# output. The checksum is pinned in README.md; a mismatch means the toolchain
# changed and the fixture should be reviewed before committing.
#
# Usage:
#   scripts/validate/fixtures/generate.sh          # write the PNG next to this script
#   OUT=/tmp/x.png scripts/validate/fixtures/generate.sh

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT="${OUT:-$HERE/jmpchat-validation-64.png}"

if ! command -v magick >/dev/null 2>&1; then
	echo "generate.sh: imagemagick (magick) is required" >&2
	exit 1
fi

magick -size 64x64 xc:'#102040' \
	-fill '#e0e0e0' -draw 'rectangle 4,4 59,59' \
	-fill '#102040' -draw 'rectangle 12,12 51,51' \
	-fill '#e0e0e0' -draw 'rectangle 20,20 43,43' \
	-fill '#102040' -draw 'rectangle 28,28 35,35' \
	-strip "$OUT"

sha256sum "$OUT"
