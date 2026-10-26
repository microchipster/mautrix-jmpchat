#!/bin/sh
set -eu

if [ -n "${BUILD_TAGS:-}" ]; then
	BINARY_NAME=mautrix-jmpchat go tool maubuild -tags "$BUILD_TAGS" "$@"
else
	BINARY_NAME=mautrix-jmpchat go tool maubuild "$@"
fi
