<!--
SPDX-FileCopyrightText: 2026 Slavi Pantaleev

SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Validation fixtures

Files here are tiny, deterministic artifacts used as payloads by the
attachment tests (`t05_attachments.sh`). They carry no personal data: the
image is drawn from ImageMagick primitives only.

| File | Size | sha256 | sha256_16 |
|---|---|---|---|
| `jmpchat-validation-64.png` | 119 B | `4b83ab14766cd76013e391a6320dabb8f812eac19d9cef3ebcd8109203bbe1b7` | `4b83ab14766cd760` |

`jmpchat-validation-64.png` is a 64x64 PNG (nested squares, two colors)
written with `-strip`, so ImageMagick embeds no timestamp or profile and
regeneration is byte-identical.

Regenerate and verify:

```bash
scripts/validate/fixtures/generate.sh
# → 4b83ab14766cd76013e391a6320dabb8f812eac19d9cef3ebcd8109203bbe1b7  .../jmpchat-validation-64.png
```

The attachment tests never log image bytes or captions: they compare
`sha256_16` prefixes, byte sizes and MIME types, and say explicitly when a
transfer was re-encoded so a hash mismatch is not mistaken for a bridge bug.
