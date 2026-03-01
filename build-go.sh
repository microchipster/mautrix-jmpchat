#!/bin/sh
set -eu

BINARY_NAME=mautrix-jmpchat go tool maubuild "$@"
