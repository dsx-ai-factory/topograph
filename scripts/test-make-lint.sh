#!/usr/bin/env bash
# Copyright 2026 NVIDIA CORPORATION
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
cp "$repo_root/Makefile" "$fixture/Makefile"
cd "$fixture"

git init -q -b main
git config user.name 'Lint regression test'
git config user.email 'lint-test@example.invalid'
git -c commit.gpgsign=false commit -q --allow-empty -m base
base=$(git rev-parse HEAD)
git update-ref refs/remotes/origin/main "$base"
git checkout -q -b feature
for n in 1 2 3; do
  git -c commit.gpgsign=false commit -q --allow-empty -m "change $n"
done

cat > lint-stub <<'EOF'
#!/bin/sh
printf '%s\n' "$@" > lint-args
EOF
chmod +x lint-stub

make lint GIT_REF=test LINTER_BIN=./lint-stub
printf '%s\n' run --new-from-rev "$base" ./... > expected-args
cmp expected-args lint-args
echo 'PASS: lint uses the branch merge base across multiple commits without master'

git update-ref -d refs/remotes/origin/main
rm lint-args
if make lint GIT_REF=test LINTER_BIN=./lint-stub > lint-output 2>&1; then
  echo 'FAIL: lint succeeded without origin/main' >&2
  exit 1
fi
test ! -e lint-args
grep -F 'origin/main is unavailable' lint-output
grep -F 'git fetch origin main:refs/remotes/origin/main' lint-output >/dev/null
echo 'PASS: a missing origin/main stops lint and prints the recovery command'

# An unrelated local branch must not substitute for the remote-tracking ref.
git branch origin/main "$base"
if make lint GIT_REF=test LINTER_BIN=./lint-stub > lint-output 2>&1; then
  echo 'FAIL: lint accepted a local branch in place of origin/main' >&2
  exit 1
fi
test ! -e lint-args
echo 'PASS: a local branch named origin/main cannot bypass the check'
