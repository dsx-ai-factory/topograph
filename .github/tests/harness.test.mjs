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

// Tests for the workflow test harness in lib/harness.mjs.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';

import {
  extractStep,
  fakeCore,
  loadLabels,
  recordingGithub,
  runBash,
  runGithubScript,
} from './lib/harness.mjs';

const SAMPLE = fileURLToPath(new URL('./fixtures/harness/sample.yaml', import.meta.url));

test('extractStep returns the run text of a named step', () => {
  const step = extractStep(SAMPLE, 'demo', 'greet');
  assert.equal(step.run, 'echo "$GREETING from the fixture"\n');
  assert.deepEqual(step.env, { GREETING: 'hello' });
  assert.equal(step.script, undefined);
});

test('extractStep returns the script, uses and with of a github-script step', () => {
  const step = extractStep(SAMPLE, 'demo', 'scripted');
  assert.equal(step.script, 'core.info(process.env.TITLE);\n');
  assert.equal(step.uses, 'actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3');
  assert.deepEqual(step.with, { script: 'core.info(process.env.TITLE);\n' });
  assert.deepEqual(step.env, { TITLE: 'fixture title' });
  assert.equal(step.run, undefined);
});

test('extractStep throws the exact message for a missing step', () => {
  assert.throws(() => extractStep(SAMPLE, 'demo', 'absent'), {
    message: `step absent not found in job demo of ${SAMPLE}`,
  });
});

test('runBash returns the exit status and the step summary', () => {
  const result = runBash('echo hi >> "$GITHUB_STEP_SUMMARY"; exit 3', {});
  assert.equal(result.status, 3);
  assert.equal(result.summary, 'hi\n');
});

test('runBash passes env and stops at a failed pipe, as the runner shell does', () => {
  const passed = runBash('echo "$HARNESS_VALUE"', { HARNESS_VALUE: 'from env' });
  assert.equal(passed.status, 0);
  assert.equal(passed.stdout, 'from env\n');

  // The runner runs `bash --noprofile --norc -eo pipefail`, so a failing
  // command early in a pipe fails the step before the echo runs.
  const piped = runBash('false | true\necho reached', {});
  assert.equal(piped.status, 1);
  assert.equal(piped.stdout, '');
});

test('runGithubScript passes github, context and core', async () => {
  const { github, calls } = recordingGithub({
    'rest.issues.get': (params) => ({ data: { number: params.issue_number } }),
  });
  const { core, log } = fakeCore();
  const context = { repo: { owner: 'octo', repo: 'demo' }, issue: { number: 7 } };

  const result = await runGithubScript(
    [
      'const { data } = await github.rest.issues.get({ ...context.repo, issue_number: context.issue.number });',
      "core.info('got ' + data.number);",
      'return data.number;',
    ].join('\n'),
    { github, context, core },
  );

  assert.equal(result, 7);
  assert.deepEqual(log.info, ['got 7']);
  assert.deepEqual(calls, [
    { route: 'rest.issues.get', params: { owner: 'octo', repo: 'demo', issue_number: 7 } },
  ]);
});

test('runGithubScript sets env for the call and restores it, even on a throw', async () => {
  const { github } = recordingGithub({});
  const { core } = fakeCore();
  delete process.env.HARNESS_ADDED;
  process.env.HARNESS_KEPT = 'before';
  try {
    const seen = await runGithubScript(
      'return [process.env.HARNESS_ADDED, process.env.HARNESS_KEPT];',
      { github, context: {}, core, env: { HARNESS_ADDED: 'inside', HARNESS_KEPT: 'inside' } },
    );
    assert.deepEqual(seen, ['inside', 'inside']);
    assert.equal(process.env.HARNESS_ADDED, undefined);
    assert.equal(process.env.HARNESS_KEPT, 'before');

    await assert.rejects(
      runGithubScript("throw new Error('boom');", {
        github, context: {}, core, env: { HARNESS_ADDED: 'inside' },
      }),
      { message: 'boom' },
    );
    assert.equal(process.env.HARNESS_ADDED, undefined);
  } finally {
    delete process.env.HARNESS_KEPT;
  }
});

test('fakeCore records each level, and the summary only when written', async () => {
  const { core, log } = fakeCore();
  core.info('i');
  core.warning('w');
  core.error('e');
  core.notice('n');
  core.setFailed('f');
  core.summary.addHeading('Title', 2).addRaw('text').addList(['a', 'b']);
  assert.deepEqual(log.summary, []);

  await core.summary.write();
  assert.deepEqual(log, {
    info: ['i'],
    warning: ['w'],
    error: ['e'],
    notice: ['n'],
    failed: ['f'],
    summary: ['<h2>Title</h2>\ntext<ul><li>a</li><li>b</li></ul>\n'],
  });
});

test('recordingGithub paginate concatenates pages', async () => {
  const items = Array.from({ length: 150 }, (_, i) => ({ number: i + 1 }));
  const { github, calls } = recordingGithub({
    'rest.pulls.list': ({ page, per_page: perPage }) => ({
      data: items.slice((page - 1) * perPage, page * perPage),
    }),
  });

  const all = await github.paginate(github.rest.pulls.list, {
    owner: 'octo', repo: 'demo', state: 'open', per_page: 100,
  });

  assert.equal(all.length, 150);
  assert.deepEqual(all, items);
  assert.equal(calls.length, 2);
  assert.deepEqual(calls.map((c) => [c.route, c.params.page]), [
    ['rest.pulls.list', 1],
    ['rest.pulls.list', 2],
  ]);
});

test('unexpected route throws', () => {
  const { github, calls } = recordingGithub({ 'rest.issues.get': () => ({ data: {} }) });
  assert.throws(() => github.rest.issues.addLabels({ labels: ['bug'] }), {
    message: 'unexpected call rest.issues.addLabels',
  });
  assert.deepEqual(calls, []);
});

test('loadLabels returns the label names declared in labels.yml', () => {
  const labels = loadLabels();
  assert.ok(labels instanceof Set);
  for (const name of ['bug', 'enhancement', 'documentation', 'question']) {
    assert.ok(labels.has(name), `labels.yml does not declare ${name}`);
  }
});
