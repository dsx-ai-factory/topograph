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

// Tests for .github/labeler.yml and .github/workflows/labeler.yaml. The area
// map is checked against the files git tracks, so a glob that matches nothing
// fails here instead of never labeling a pull request. The size step's script
// runs against a recorded fake of the GitHub API.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { join, resolve } from 'node:path';

import {
  GITHUB_DIR,
  extractStep,
  fakeCore,
  loadLabels,
  loadWorkflow,
  recordingGithub,
  runGithubScript,
} from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'labeler.yaml');
const LABELER = join(GITHUB_DIR, 'labeler.yml');
const REPO_ROOT = resolve(GITHUB_DIR, '..');
const REPO = { owner: 'dsx-ai-factory', repo: 'topograph' };
const NUMBER = 42;

// The area map from the governance design (U6), verbatim.
const AREAS = {
  'area/providers': ['pkg/providers/**'],
  'area/engines': ['pkg/engines/**', 'pkg/translate/**'],
  'area/topology': ['pkg/topology/**'],
  'area/accelerator': ['pkg/accelerator/**'],
  'area/chart': ['charts/**'],
  'area/node-observer': ['pkg/node_observer/**', 'cmd/node-observer/**'],
  'area/node-data-broker': ['cmd/node-data-broker/**'],
  'area/api': ['pkg/server/**', 'cmd/topograph/**'],
  'area/infiniband': ['pkg/ib/**'],
  'area/docs': ['docs/**', 'fern/**', '*.md'],
  'area/ci': ['.github/**'],
  'area/build': ['Dockerfile', 'Makefile', 'deb/**', 'rpmbuild/**', 'scripts/**'],
  'area/tests': ['tests/**', 'pkg/test/**', 'demos/**'],
};

// The tracked files a git pathspec glob matches. With :(glob), `*` does not
// cross a `/` and `**/` spans directories, as in the labeler's minimatch.
function trackedFiles(glob) {
  const result = spawnSync('git', ['-C', REPO_ROOT, 'ls-files', '-z', '--', `:(glob)${glob}`], { encoding: 'utf8' });
  if (result.error) throw new Error(`cannot run git: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`git ls-files failed for ${glob}: ${result.stderr.trim()}`);
  return result.stdout.split('\0').filter(Boolean);
}

// Every any-glob-to-any-file entry in labeler.yml, with its label.
function labelerGlobs(config) {
  return Object.entries(config ?? {}).flatMap(([label, rules]) =>
    (rules ?? []).flatMap((rule) =>
      (rule?.['changed-files'] ?? []).flatMap((match) =>
        [match?.['any-glob-to-any-file'] ?? []].flat().map((glob) => ({ label, glob })),
      ),
    ),
  );
}

// A fake GitHub whose pulls.get serves the pull request with `labels`, the
// labels it has when the step runs. `routes` replaces individual handlers.
function fakeGithub(labels, routes = {}) {
  return recordingGithub({
    'rest.pulls.get': ({ pull_number: number }) => ({ data: { number, labels: labels.map((name) => ({ name })) } }),
    'rest.issues.addLabels': ({ labels: added }) => ({ data: [...labels, ...added].map((name) => ({ name })) }),
    'rest.issues.removeLabel': () => ({ data: [] }),
    ...routes,
  });
}

// Runs the size step against `github` for a pull request with these line
// counts. `eventLabels` is the label list the event reported.
async function runSizeStep(github, { additions, deletions, eventLabels }) {
  const { script } = extractStep(WORKFLOW, 'label', 'size');
  const { core } = fakeCore();
  const context = {
    repo: REPO,
    payload: {
      pull_request: { number: NUMBER, additions, deletions, labels: eventLabels.map((name) => ({ name })) },
    },
  };
  await runGithubScript(script, { github, context, core });
}

// Runs the size step for a pull request that has `labels` when the step runs
// and returns the API calls it made. By default the event reported the same
// labels.
async function runSize({ additions, deletions, labels = [], eventLabels = labels }) {
  const { github, calls } = fakeGithub(labels);
  await runSizeStep(github, { additions, deletions, eventLabels });
  return { calls };
}

// The calls that change the pull request's labels.
function writes(calls) {
  return calls.filter(({ route }) => route.startsWith('rest.issues.'));
}

test('every labeler glob matches a tracked file', () => {
  // Controls: the helper finds a known file and finds nothing for a path
  // that does not exist, so an empty result below means the glob is wrong.
  assert.deepEqual(trackedFiles('Makefile'), ['Makefile']);
  assert.deepEqual(trackedFiles('pkg/no-such-directory/**'), []);

  const globs = labelerGlobs(loadWorkflow(LABELER));
  assert.ok(globs.length > 0, 'no any-glob-to-any-file entry found in labeler.yml');
  const unmatched = globs.filter(({ glob }) => trackedFiles(glob).length === 0).map(({ label, glob }) => `${label}: ${glob}`);
  assert.deepEqual(unmatched, []);
});

test('labeler.yml maps each area to its globs in the v5+ changed-files syntax', () => {
  const expected = Object.fromEntries(
    Object.entries(AREAS).map(([label, globs]) => [label, [{ 'changed-files': [{ 'any-glob-to-any-file': globs }] }]]),
  );
  assert.deepEqual(loadWorkflow(LABELER), expected);
});

test('the paths step runs the pinned labeler with sync-labels on opened, synchronize and reopened', () => {
  const wf = loadWorkflow(WORKFLOW);
  assert.deepEqual(wf.on, { pull_request_target: { types: ['opened', 'synchronize', 'reopened'] } });
  assert.deepEqual(wf.jobs.label.permissions, { contents: 'read', 'pull-requests': 'write' });
  const paths = extractStep(WORKFLOW, 'label', 'paths');
  assert.equal(paths.uses, 'actions/labeler@bf12e9b00b37c5c0ca2b87b79b2daf7891dbda13');
  assert.deepEqual(paths.with, { 'sync-labels': true });
});

test('size thresholds', async () => {
  const cases = [
    [9, 0, 'size/XS'],
    [5, 5, 'size/S'],
    [49, 0, 'size/S'],
    [50, 0, 'size/M'],
    [199, 0, 'size/M'],
    [200, 0, 'size/L'],
    [499, 0, 'size/L'],
    [500, 0, 'size/XL'],
  ];
  const actual = [];
  for (const [additions, deletions] of cases) {
    const { calls } = await runSize({ additions, deletions });
    const added = writes(calls).map(({ route, params }) => [route, params]);
    actual.push([additions, deletions, added]);
  }
  assert.deepEqual(
    actual,
    cases.map(([additions, deletions, label]) => [
      additions,
      deletions,
      [['rest.issues.addLabels', { ...REPO, issue_number: NUMBER, labels: [label] }]],
    ]),
  );
});

test('every size label the step applies is declared in labels.yml', async () => {
  // labels.test.mjs cannot see these: the script passes a variable, not a
  // literal, to addLabels.
  const applied = [];
  for (const additions of [0, 10, 50, 200, 500]) {
    const { calls } = await runSize({ additions, deletions: 0 });
    applied.push(...writes(calls).flatMap(({ params }) => params.labels));
  }
  assert.deepEqual(applied, ['size/XS', 'size/S', 'size/M', 'size/L', 'size/XL']);
  const declared = loadLabels();
  assert.deepEqual(applied.filter((label) => !declared.has(label)), []);
});

test('size relabel removes the old size label only', async () => {
  const { calls } = await runSize({ additions: 100, deletions: 20, labels: ['size/S', 'area/docs'] });

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.addLabels', params: { ...REPO, issue_number: NUMBER, labels: ['size/M'] } },
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: NUMBER, name: 'size/S' } },
  ]);
});

test('every other size label is removed', async () => {
  const { calls } = await runSize({ additions: 300, deletions: 0, labels: ['size/XS', 'size/L', 'bug', 'size/XL'] });

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: NUMBER, name: 'size/XS' } },
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: NUMBER, name: 'size/XL' } },
  ]);
});

test('unchanged size makes no write call', async () => {
  const { calls } = await runSize({ additions: 60, deletions: 40, labels: ['area/docs', 'size/M'] });

  assert.deepEqual(writes(calls), []);
  assert.deepEqual(calls, [{ route: 'rest.pulls.get', params: { ...REPO, pull_number: NUMBER } }]);
});

test('labels are read when the step runs, not from the event', async () => {
  // The event still lists size/S, but a later run already moved the PR to
  // size/M. Trusting the event would remove a label the PR no longer has
  // (GitHub answers 404) and add one it already has.
  const { calls } = await runSize({
    additions: 100,
    deletions: 0,
    labels: ['size/M'],
    eventLabels: ['size/S'],
  });

  assert.deepEqual(writes(calls), []);
});

test('missing size label surfaces as job failure', async () => {
  // A token holding only pull-requests: write cannot create a label, so
  // GitHub answers 403 when size/M does not exist.
  const { github, calls } = fakeGithub(['size/S'], {
    'rest.issues.addLabels': () => {
      throw Object.assign(new Error('You do not have permission to create labels on this repository.'), {
        status: 403,
      });
    },
  });

  await assert.rejects(runSizeStep(github, { additions: 100, deletions: 0, eventLabels: ['size/S'] }), {
    status: 403,
    message: 'You do not have permission to create labels on this repository.',
  });
  // The add comes first, so a failed add leaves the old size label on.
  assert.deepEqual(writes(calls).map(({ route }) => route), ['rest.issues.addLabels']);
});
