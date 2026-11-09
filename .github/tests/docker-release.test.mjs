// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Tests for the release evidence steps in .github/workflows/docker.yml: the
// release gate, the platform manifest lookup, SBOM and OpenVEX attestation and
// index signing, and verification. Each test runs the workflow's own `run:`
// block with docker, syft, cosign and timeout replaced by PATH shims, so
// nothing reaches a registry or Sigstore. The shims append every call to a
// log the test reads back.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { GITHUB_DIR, extractStep, runBash } from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'docker.yml');
const SECURITY = join(GITHUB_DIR, '..', 'SECURITY.md');
const IMAGE = 'nvcr.io/1070195845945670/topograph/topograph';
const INDEX = `sha256:${'a'.repeat(64)}`;
const AMD64 = `sha256:${'1'.repeat(64)}`;
const ARM64 = `sha256:${'2'.repeat(64)}`;
const PLATFORM_DIGESTS = `linux/amd64=${AMD64} linux/arm64=${ARM64}`;
const WORKFLOW_REF = 'dsx-ai-factory/topograph/.github/workflows/docker.yml@refs/heads/v1.2.0';
const IDENTITY = `https://github.com/${WORKFLOW_REF}`;

const SHIMS = {
  // Drops `timeout`'s flags and duration and runs the command, so the steps
  // run the same on macOS, which has no coreutils timeout.
  timeout: `while [[ "$1" == --* ]]; do shift; done; shift; exec "$@"`,
  docker: `echo "docker $*" >>"$CALLS"; cat "$INDEX_FIXTURE"`,
  // Writes the SBOM a real Syft run would, for the digest it was given.
  syft: `echo "syft $*" >>"$CALLS"
digest="\${2##*@}"; out="\${4#*=}"
printf '{"bomFormat":"CycloneDX","specVersion":"%s","metadata":{"component":{"type":"container","version":"%s"}}}' \\
  "\${SYFT_SPEC_VERSION:-1.6}" "\${digest}" >"\${out}"`,
  // verify-attestation prints one DSSE envelope whose statement names the
  // digest of the reference it was given, or COSIGN_SUBJECT when set.
  cosign: `echo "cosign $*" >>"$CALLS"
if [[ "$1" == verify-attestation ]]; then
  ref="\${*: -1}"; hex="\${COSIGN_SUBJECT:-\${ref##*@sha256:}}"
  payload="$(printf '{"subject":[{"digest":{"sha256":"%s"}}]}' "\${hex}" | base64 | tr -d '\\n')"
  printf '{"payload":"%s"}\\n' "\${payload}"
fi`,
};

// Runs step `id` of the build job with `env`, the shims first on PATH, and
// the files in `files` written to the scratch directory. Returns the bash
// result plus the step outputs and the logged calls.
function runStep(id, env = {}, files = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'docker-release-'));
  try {
    const bin = join(dir, 'bin');
    mkdirSync(bin);
    for (const [name, body] of Object.entries(SHIMS)) {
      writeFileSync(join(bin, name), `#!/usr/bin/env bash\n${body}\n`);
      chmodSync(join(bin, name), 0o755);
    }
    for (const [name, content] of Object.entries(files)) writeFileSync(join(dir, name), content);
    const calls = join(dir, 'calls');
    const output = join(dir, 'output');
    writeFileSync(output, '');
    const resolved = Object.fromEntries(
      Object.entries(env).map(([key, value]) => [key, String(value).replaceAll('{dir}', dir)]),
    );
    const result = runBash(extractStep(WORKFLOW, 'build', id).run, {
      PATH: `${bin}:${process.env.PATH}`,
      CALLS: calls,
      GITHUB_OUTPUT: output,
      RUNNER_TEMP: dir,
      SYFT: join(bin, 'syft'),
      ...resolved,
    });
    const outputs = Object.fromEntries(
      readFileSync(output, 'utf8')
        .split('\n')
        .filter(Boolean)
        .map((line) => [line.slice(0, line.indexOf('=')), line.slice(line.indexOf('=') + 1)]),
    );
    const logged = existsSync(calls) ? readFileSync(calls, 'utf8').split('\n').filter(Boolean) : [];
    return { ...result, outputs, calls: logged };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

function manifest(digest, os, architecture) {
  return { mediaType: 'application/vnd.oci.image.manifest.v1+json', digest, platform: { os, architecture } };
}

function indexOf(...manifests) {
  return JSON.stringify({ schemaVersion: 2, mediaType: 'application/vnd.oci.image.index.v1+json', manifests });
}

const OPENVEX = JSON.stringify({ '@context': 'https://openvex.dev/ns/v0.2.0', statements: [] });

test('only a dispatch from a vX.Y.Z branch publishes release evidence', () => {
  const cases = [
    ['branch', 'v1.2.0', 'true'],
    ['branch', 'v10.20.30', 'true'],
    ['tag', 'v1.2.0', 'false'],
    ['branch', 'main', 'false'],
    ['branch', 'v1.2', 'false'],
    ['branch', 'v1.2.0-rc.1', 'false'],
    ['branch', 'release-1.2', 'false'],
  ];
  for (const [refType, refName, expected] of cases) {
    const result = runStep('evidence', { REF_TYPE: refType, REF_NAME: refName });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.outputs.publish, expected, `${refType} ${refName}`);
  }
});

test('platform lookup returns one digest per platform and skips attestation entries', () => {
  const index = indexOf(
    manifest(AMD64, 'linux', 'amd64'),
    manifest(ARM64, 'linux', 'arm64'),
    manifest(`sha256:${'3'.repeat(64)}`, 'unknown', 'unknown'),
  );
  const result = runStep(
    'platforms',
    { IMAGE_NAME: IMAGE, INDEX_DIGEST: INDEX, PLATFORMS: 'linux/amd64,linux/arm64', INDEX_FIXTURE: '{dir}/index.json' },
    { 'index.json': index },
  );
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.equal(result.outputs.digests, PLATFORM_DIGESTS);
  assert.deepEqual(result.calls, [`docker buildx imagetools inspect --raw ${IMAGE}@${INDEX}`]);
});

test('platform lookup fails on a missing or duplicated platform', () => {
  const cases = {
    missing: indexOf(manifest(AMD64, 'linux', 'amd64')),
    duplicated: indexOf(manifest(AMD64, 'linux', 'amd64'), manifest(AMD64, 'linux', 'amd64'), manifest(ARM64, 'linux', 'arm64')),
    'not an index': JSON.stringify({ schemaVersion: 2, layers: [] }),
  };
  for (const [name, index] of Object.entries(cases)) {
    const result = runStep(
      'platforms',
      { IMAGE_NAME: IMAGE, INDEX_DIGEST: INDEX, PLATFORMS: 'linux/amd64,linux/arm64', INDEX_FIXTURE: '{dir}/index.json' },
      { 'index.json': index },
    );
    assert.notEqual(result.status, 0, name);
    assert.match(result.stdout, /does not list exactly one linux\/(amd64|arm64) manifest/, name);
  }
});

function attestEnv(overrides = {}) {
  return { IMAGE_NAME: IMAGE, INDEX_DIGEST: INDEX, PLATFORM_DIGESTS, OPENVEX_FILE: '{dir}/openvex.json', ...overrides };
}

function cosignCalls(calls) {
  return calls.filter((call) => call.startsWith('cosign '));
}

test('attest publishes per-platform SBOMs, then OpenVEX on the index, and signs the index last', () => {
  const result = runStep('attest', attestEnv(), { 'openvex.json': OPENVEX });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.equal(result.outputs.openvex, 'true');
  const steps = cosignCalls(result.calls).map((call) => {
    const words = call.split(' ');
    return [words[1], words.includes('--type') ? words[words.indexOf('--type') + 1] : '-', words.at(-3)];
  });
  assert.deepEqual(steps, [
    ['attest', 'cyclonedx', `${IMAGE}@${AMD64}`],
    ['attest', 'cyclonedx', `${IMAGE}@${ARM64}`],
    ['attest', 'openvex', `${IMAGE}@${INDEX}`],
    ['sign', '-', `${IMAGE}@${INDEX}`],
  ]);
  for (const call of cosignCalls(result.calls)) assert.match(call, / --yes --new-bundle-format=true$/);
  // Syft reads each platform from the registry, never from the local daemon.
  assert.deepEqual(
    result.calls.filter((call) => call.startsWith('syft ')).map((call) => call.split(' ')[2]),
    [`registry:${IMAGE}@${AMD64}`, `registry:${IMAGE}@${ARM64}`],
  );
});

test('attest skips OpenVEX when the document does not exist, and still signs', () => {
  const result = runStep('attest', attestEnv());
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.equal(result.outputs.openvex, 'false');
  assert.match(result.stdout, /::notice::.*does not exist; skipping the OpenVEX attestation/);
  const calls = cosignCalls(result.calls);
  assert.ok(!calls.some((call) => call.includes('--type openvex')));
  assert.match(calls.at(-1), /^cosign sign /);
});

test('attest publishes nothing when an SBOM or the OpenVEX document is invalid', () => {
  const badSbom = runStep('attest', attestEnv({ SYFT_SPEC_VERSION: '1.5' }), { 'openvex.json': OPENVEX });
  assert.notEqual(badSbom.status, 0);
  assert.match(badSbom.stdout, /is not a CycloneDX 1\.6 SBOM/);
  assert.deepEqual(cosignCalls(badSbom.calls), []);

  const badVex = runStep('attest', attestEnv(), { 'openvex.json': JSON.stringify({ statements: [] }) });
  assert.notEqual(badVex.status, 0);
  assert.match(badVex.stdout, /is not an OpenVEX document/);
  assert.deepEqual(cosignCalls(badVex.calls), []);
});

function verifyEnv(overrides = {}) {
  return { IMAGE_NAME: IMAGE, INDEX_DIGEST: INDEX, PLATFORM_DIGESTS, OPENVEX: 'true', GITHUB_WORKFLOW_REF: WORKFLOW_REF, ...overrides };
}

test('verify checks every piece of evidence against the exact workflow identity', () => {
  const result = runStep('verify', verifyEnv());
  assert.equal(result.status, 0, result.stdout + result.stderr);
  const calls = cosignCalls(result.calls);
  const checked = calls.map((call) => {
    const words = call.split(' ');
    return [words[1], words.includes('--type') ? words[words.indexOf('--type') + 1] : '-', words.at(-1)];
  });
  assert.deepEqual(checked, [
    ['verify', '-', `${IMAGE}@${INDEX}`],
    ['verify-attestation', 'slsaprovenance1', `${IMAGE}@${INDEX}`],
    ['verify-attestation', 'openvex', `${IMAGE}@${INDEX}`],
    ['verify-attestation', 'cyclonedx', `${IMAGE}@${AMD64}`],
    ['verify-attestation', 'cyclonedx', `${IMAGE}@${ARM64}`],
  ]);
  for (const call of calls) {
    assert.ok(call.includes(` --certificate-identity ${IDENTITY} `), call);
    assert.ok(!call.includes('--certificate-identity-regexp'), call);
  }
});

test('verify skips OpenVEX when none was attested', () => {
  const result = runStep('verify', verifyEnv({ OPENVEX: 'false' }));
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.ok(!result.calls.some((call) => call.includes('--type openvex')));
});

test('verify fails when an attestation names another digest', () => {
  const result = runStep('verify', verifyEnv({ COSIGN_SUBJECT: 'f'.repeat(64) }));
  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /does not name that digest/);
});

test('the SECURITY.md admission policy accepts release branches of docker.yml only', () => {
  const match = readFileSync(SECURITY, 'utf8').match(/subjectRegExp: '([^']+)'/);
  assert.ok(match, 'SECURITY.md has a ClusterImagePolicy subjectRegExp');
  const pattern = match[1];
  assert.ok(pattern.startsWith('^') && pattern.endsWith('$') && !pattern.includes('|'), pattern);
  const policy = new RegExp(pattern);
  const workflow = 'https://github.com/dsx-ai-factory/topograph/.github/workflows/docker.yml';
  assert.ok(policy.test(`${workflow}@refs/heads/v1.2.0`));
  for (const identity of [
    `${workflow}@refs/tags/v1.2.0`,
    `${workflow}@refs/heads/main`,
    `${workflow}@refs/heads/v1.2.0-rc.1`,
    'https://github.com/dsx-ai-factory/topograph/.github/workflows/helm-release.yaml@refs/heads/v1.2.0',
    'https://github.com/someone/topograph/.github/workflows/docker.yml@refs/heads/v1.2.0',
  ]) {
    assert.ok(!policy.test(identity), identity);
  }
});
