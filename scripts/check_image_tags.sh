#!/bin/sh
set -eu

printf '%s\n' "$APP_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'
case "$GITHUB_REF" in
  refs/heads/dev)
    expected="$IMAGE:dev"
    ;;
  refs/heads/main)
    expected=$(printf '%s\n' "$IMAGE:latest" "$IMAGE:$APP_VERSION" | sort)
    ;;
  refs/tags/v*)
    test "$GITHUB_REF" = "refs/tags/v$APP_VERSION"
    expected="$IMAGE:$APP_VERSION"
    ;;
  *)
    echo "This ref cannot publish container images" >&2
    exit 1
    ;;
esac
test "$(printf '%s\n' "$PUBLISH_TAGS" | sort)" = "$expected"
