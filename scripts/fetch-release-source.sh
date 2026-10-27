#!/usr/bin/env bash
set -euo pipefail
: "${REF:?REF is required}"
case "$REF" in
  refs/heads/main) branch=main ;;
  refs/heads/eks-staging|refs/tags/v*-rc.*) branch=eks-staging ;;
  refs/tags/v*) branch=main ;;
  *) echo 'Unsupported publication source' >&2; exit 1 ;;
esac
git -C "${REPOSITORY_DIR:-.}" fetch origin "$branch"
