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

// Tests for the needs-rebase script in .github/workflows/conflict-check.yaml.
// Each test runs the workflow's own script against a recorded fake of the
// GitHub API. As on GitHub, pulls.list does not return `mergeable`, so the
// script has to read it from pulls.get.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';

import { GITHUB_DIR, extractStep, fakeCore, loadWorkflow, recordingGithub, runGithubScript } from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'conflict-check.yaml');
const REPO = { owner: 'dsx-ai-factory', repo: 'topograph' };

// One open pull request as pulls.get returns it.
function pr(number, { mergeable, draft = false, labels = [], login = `author${number}` }) {
  return { number, draft, mergeable, labels: labels.map((name) => ({ name })), user: { login } };
}

// A fake GitHub holding `prs`. pulls.list serves them a page at a time
// without `mergeable`; pulls.get serves each one in full. `routes` replaces
// individual handlers.
function fakeGithub(prs, routes = {}) {
  return recordingGithub({
    'rest.pulls.list': ({ page = 1, per_page: perPage = 30 }) => ({
      data: prs.slice((page - 1) * perPage, page * perPage).map(({ mergeable, ...listed }) => listed),
    }),
    'rest.pulls.get': ({ pull_number: number }) => ({ data: prs.find((p) => p.number === number) }),
    'rest.issues.addLabels': ({ labels }) => ({ data: labels.map((name) => ({ name })) }),
    'rest.issues.removeLabel': () => ({ data: [] }),
    'rest.issues.createComment': () => ({ data: { id: 1 } }),
    ...routes,
  });
}

async function run(github) {
  const { script } = extractStep(WORKFLOW, 'conflicts', 'check');
  const { core, log } = fakeCore();
  await runGithubScript(script, { github, context: { repo: REPO }, core });
  return log;
}

// The calls that change a pull request: labels and comments.
function writes(calls) {
  return calls.filter(({ route }) => route.startsWith('rest.issues.'));
}

// The pull request numbers the script read with pulls.get, in order.
function fetched(calls) {
  return calls.filter(({ route }) => route === 'rest.pulls.get').map(({ params }) => params.pull_number);
}

test('conflicted PR gets label and one comment', async () => {
  const { github, calls } = fakeGithub([pr(7, { mergeable: false, login: 'octocat' })]);

  await run(github);

  const changed = writes(calls);
  assert.deepEqual(changed.map(({ route }) => route), ['rest.issues.addLabels', 'rest.issues.createComment']);
  assert.deepEqual(changed[0].params, { ...REPO, issue_number: 7, labels: ['needs-rebase'] });
  const { body, ...target } = changed[1].params;
  assert.deepEqual(target, { ...REPO, issue_number: 7 });
  assert.ok(body.startsWith('@octocat '), `comment starts ${JSON.stringify(body.slice(0, 20))}`);
  assert.ok(body.includes('`main`'), 'comment does not name `main`');
  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.pulls.get').map(({ params }) => params),
    [{ ...REPO, pull_number: 7 }],
  );
});

test('clean PR with label loses it', async () => {
  const { github, calls } = fakeGithub([pr(8, { mergeable: true, labels: ['bug', 'needs-rebase'] })]);

  await run(github);

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: 8, name: 'needs-rebase' } },
  ]);
});

test('unknown mergeable does nothing', async () => {
  // One unlabeled and one labeled: treating null as a conflict would label
  // #9, and treating it as clean would strip the label from #10.
  const { github, calls } = fakeGithub([
    pr(9, { mergeable: null }),
    pr(10, { mergeable: null, labels: ['needs-rebase'] }),
  ]);

  const log = await run(github);

  assert.deepEqual(writes(calls), []);
  assert.deepEqual(fetched(calls), [9, 10]);
  assert.deepEqual(log.info, [
    '#9: GitHub has not computed mergeability yet, skipped',
    '#10: GitHub has not computed mergeability yet, skipped',
  ]);
});

test('already labeled conflicted PR gets no second comment', async () => {
  const { github, calls } = fakeGithub([pr(11, { mergeable: false, labels: ['needs-rebase'] })]);

  await run(github);

  assert.deepEqual(writes(calls), []);
  assert.deepEqual(fetched(calls), [11]);
});

test('drafts are skipped', async () => {
  // The draft conflicts, so reading it would label it.
  const { github, calls } = fakeGithub([
    pr(12, { mergeable: false, draft: true }),
    pr(13, { mergeable: true }),
  ]);

  await run(github);

  assert.deepEqual(fetched(calls), [13]);
  assert.deepEqual(writes(calls), []);
});

test('second page of open PRs is processed', async () => {
  const prs = Array.from({ length: 150 }, (_, i) => pr(i + 1, { mergeable: i + 1 !== 150 }));
  const { github, calls } = fakeGithub(prs);

  await run(github);

  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.pulls.list').map(({ params }) => params),
    [
      { ...REPO, state: 'open', per_page: 100, page: 1 },
      { ...REPO, state: 'open', per_page: 100, page: 2 },
    ],
  );
  assert.equal(fetched(calls).length, 150);
  assert.deepEqual(
    writes(calls).map(({ route, params }) => [route, params.issue_number]),
    [
      ['rest.issues.addLabels', 150],
      ['rest.issues.createComment', 150],
    ],
  );
});

test('missing label surfaces as job failure', async () => {
  // A token holding only pull-requests: write cannot create a label, so
  // GitHub answers 403 when needs-rebase does not exist.
  const { github, calls } = fakeGithub([pr(14, { mergeable: false })], {
    'rest.issues.addLabels': () => {
      throw Object.assign(new Error('You do not have permission to create labels on this repository.'), {
        status: 403,
      });
    },
  });

  await assert.rejects(run(github), {
    status: 403,
    message: 'You do not have permission to create labels on this repository.',
  });
  // The comment would tell the author about a label the PR does not have.
  assert.deepEqual(writes(calls).map(({ route }) => route), ['rest.issues.addLabels']);
});

test('job runs only in the upstream repository', () => {
  // A fork with Actions enabled would otherwise label and comment on its own
  // PRs. U0's guarded jobs invariant does not cover push-triggered jobs.
  assert.equal(loadWorkflow(WORKFLOW).jobs.conflicts.if, "github.repository == 'dsx-ai-factory/topograph'");
});
