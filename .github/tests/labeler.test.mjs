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
// map is checked against the files git tracks, with the labeler's own glob
// rules, so a glob that matches nothing fails here instead of never labeling
// a pull request. The size step's script runs against a recorded fake of the
// GitHub API.

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

// Every file git tracks. The list is taken without a pathspec, because a git
// pathspec also matches every file under a bare directory name such as
// `charts`, and the labeler does not.
function trackedFiles() {
  const result = spawnSync('git', ['-C', REPO_ROOT, 'ls-files', '-z'], { encoding: 'utf8' });
  if (result.error) throw new Error(`cannot run git: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`git ls-files failed: ${result.stderr.trim()}`);
  return result.stdout.split('\0').filter(Boolean);
}

// Whether the labeler matches the changed file `file` with `glob`.
// actions/labeler v7.0.0 (bf12e9b) calls new Minimatch(glob, { dot }).match(file)
// (src/changedFiles.ts:229 and 239), and dot defaults to true (action.yml:19);
// labeler.yaml does not set it. Node's path.matchesGlob is no substitute: it
// fixes dot at false, and on macOS its wildcards ignore case. This matcher
// knows only the syntax labeler.yml uses and throws on the rest, so a glob
// written in any other syntax fails here until the matcher learns it.
function globMatches(glob, file) {
  const pattern = glob.split('/');
  if (/[?[\]{}()!#\\]/.test(glob) || pattern.some((s) => ['', '.', '..'].includes(s) || (s.includes('**') && s !== '**'))) {
    throw new Error(`globMatches does not implement ${glob}: it handles literal segments, * inside a segment, and ** as a whole segment`);
  }
  const segment = (s) => new RegExp(`^${s.split('*').map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('[^/]*')}$`);
  const matchFrom = (rest, parts) => {
    if (rest.length === 0) return parts.length === 0;
    const [head, ...tail] = rest;
    if (head === '**') {
      // A trailing ** needs at least one segment and any other ** may match
      // none: in minimatch 10.2.5 `a/**` misses `a` but `a/**/b` matches `a/b`.
      for (let skip = tail.length === 0 ? 1 : 0; skip <= parts.length; skip++) {
        if (matchFrom(tail, parts.slice(skip))) return true;
      }
      return false;
    }
    return parts.length > 0 && segment(head).test(parts[0]) && matchFrom(tail, parts.slice(1));
  };
  return matchFrom(pattern, file.split('/'));
}

// The entries in `globs` that match no tracked file, as "label: glob".
function unmatchedGlobs(globs) {
  const files = trackedFiles();
  return globs.filter(({ glob }) => !files.some((file) => globMatches(glob, file))).map(({ label, glob }) => `${label}: ${glob}`);
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

// A fake GitHub whose pulls.get serves the pull request as it is when the
// step runs: `additions` and `deletions` lines changed, carrying `labels`.
// `routes` replaces individual handlers.
function fakeGithub({ additions, deletions, labels }, routes = {}) {
  return recordingGithub({
    'rest.pulls.get': ({ pull_number: number }) => ({
      data: { number, additions, deletions, labels: labels.map((name) => ({ name })) },
    }),
    'rest.issues.addLabels': ({ labels: added }) => ({ data: [...labels, ...added].map((name) => ({ name })) }),
    'rest.issues.removeLabel': () => ({ data: [] }),
    ...routes,
  });
}

// Runs the size step against `github` and returns what it logged. `event`
// is the pull request as the event reported it: its line counts and labels.
async function runSizeStep(github, event) {
  const { script } = extractStep(WORKFLOW, 'label', 'size');
  const { core, log } = fakeCore();
  const context = {
    repo: REPO,
    payload: {
      pull_request: {
        number: NUMBER,
        additions: event.additions,
        deletions: event.deletions,
        labels: event.labels.map((name) => ({ name })),
      },
    },
  };
  await runGithubScript(script, { github, context, core });
  return { log };
}

// Runs the size step for a pull request that has these line counts and
// `labels` when the step runs, and returns the API calls it made. `event`
// overrides what the event reported; by default it reported the same.
async function runSize({ additions, deletions, labels = [], event = {} }) {
  const { github, calls } = fakeGithub({ additions, deletions, labels });
  await runSizeStep(github, { additions, deletions, labels, ...event });
  return { calls };
}

// The calls that change the pull request's labels.
function writes(calls) {
  return calls.filter(({ route }) => route.startsWith('rest.issues.'));
}

test('every labeler glob matches a tracked file', () => {
  // Controls: a known file is matched and a path that does not exist is
  // not, so an entry reported below means the glob is wrong.
  assert.deepEqual(
    unmatchedGlobs([
      { label: 'control', glob: 'Makefile' },
      { label: 'control', glob: 'pkg/no-such-directory/**' },
    ]),
    ['control: pkg/no-such-directory/**'],
  );

  const globs = labelerGlobs(loadWorkflow(LABELER));
  assert.ok(globs.length > 0, 'no any-glob-to-any-file entry found in labeler.yml');
  assert.deepEqual(unmatchedGlobs(globs), []);
});

test('a glob naming a directory matches none of the files under it', () => {
  // charts/ holds tracked files, but the labeler compares each glob with
  // whole file paths, so `charts` never labels a pull request.
  assert.deepEqual(
    unmatchedGlobs([
      { label: 'area/chart', glob: 'charts' },
      { label: 'area/chart', glob: 'charts/**' },
    ]),
    ['area/chart: charts'],
  );
});

test('globMatches follows the labeler: minimatch with dot: true', () => {
  const cases = [
    // A directory name is not a prefix.
    ['charts', 'charts/topograph/Chart.yaml', false],
    ['charts/**', 'charts/topograph/Chart.yaml', true],
    // A trailing ** needs at least one segment; an inner ** may match none.
    ['charts/**', 'charts', false],
    ['docs/**/index.md', 'docs/index.md', true],
    ['docs/**/index.md', 'docs/engines/index.md', true],
    // * stays inside one segment and must cover all of it.
    ['*.md', 'README.md', true],
    ['*.md', 'docs/overview.md', false],
    ['pkg/test/**', 'pkg/testing/fake.go', false],
    ['Dockerfile', 'Dockerfile.dev', false],
    // dot: true, the action's default, lets wildcards match a leading dot.
    ['**/labels.yml', '.github/labels.yml', true],
    ['*', '.gitignore', true],
    // Case matters, as it does on the Linux runner.
    ['MAKE*', 'Makefile', false],
  ];
  assert.deepEqual(
    cases.map(([glob, file]) => [glob, file, globMatches(glob, file)]),
    cases,
  );
});

test('globMatches rejects glob syntax it does not implement', () => {
  for (const glob of ['docs/{api,overview}.md', '!docs/**', 'docs/?.md', 'docs/[a-z]*.md', '#docs/**', 'docs/**.md', 'charts/', './charts/**']) {
    assert.throws(() => globMatches(glob, 'docs/api.md'), {
      message: `globMatches does not implement ${glob}: it handles literal segments, * inside a segment, and ** as a whole segment`,
    });
  }
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

test('the size step runs after a failed paths step but not after a cancel', () => {
  // Without a status function a step runs only on success(), so a failed
  // paths step (an area/* label that does not exist) would also cost the
  // pull request its size label. !cancelled() still skips the step when a
  // newer run cancels this one; always() would not.
  const steps = loadWorkflow(WORKFLOW).jobs.label.steps;
  assert.deepEqual(
    steps.map((step) => [step.id, step.if]),
    [
      ['paths', undefined],
      ['size', '${{ !cancelled() }}'],
    ],
  );
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
    event: { labels: ['size/S'] },
  });

  assert.deepEqual(writes(calls), []);
});

test('line counts are read when the step runs, not from the event', async () => {
  // A re-run replays its original event, and runs for the same pull request
  // need not start in event order, so the event's counts can be stale. Here
  // the event saw 20 lines but the pull request has grown to 300 and already
  // carries size/L; trusting the event would put size/S back.
  const grown = await runSize({
    additions: 280,
    deletions: 20,
    labels: ['size/L'],
    event: { additions: 20, deletions: 0 },
  });
  assert.deepEqual(writes(grown.calls), []);

  // The other way: the event saw 300 lines, the pull request is down to 20.
  const shrunk = await runSize({
    additions: 15,
    deletions: 5,
    labels: ['size/L'],
    event: { additions: 300, deletions: 0 },
  });
  assert.deepEqual(writes(shrunk.calls), [
    { route: 'rest.issues.addLabels', params: { ...REPO, issue_number: NUMBER, labels: ['size/S'] } },
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: NUMBER, name: 'size/L' } },
  ]);
});

test('missing size label surfaces as job failure', async () => {
  // A token holding only pull-requests: write cannot create a label, so
  // GitHub answers 403 when size/M does not exist.
  const pr = { additions: 100, deletions: 0, labels: ['size/S'] };
  const { github, calls } = fakeGithub(pr, {
    'rest.issues.addLabels': () => {
      throw Object.assign(new Error('You do not have permission to create labels on this repository.'), {
        status: 403,
      });
    },
  });

  await assert.rejects(runSizeStep(github, pr), {
    status: 403,
    message: 'You do not have permission to create labels on this repository.',
  });
  // The add comes first, so a failed add leaves the old size label on.
  assert.deepEqual(writes(calls).map(({ route }) => route), ['rest.issues.addLabels']);
});

test('a size label already removed by someone else does not fail the job', async () => {
  // Another run or a person removed size/S after pulls.get read the labels,
  // so GitHub answers 404 (issues/remove-label). size/M is already on, so the
  // step logs it and goes on to the next stale label.
  const pr = { additions: 100, deletions: 0, labels: ['size/S', 'size/XL'] };
  const { github, calls } = fakeGithub(pr, {
    'rest.issues.removeLabel': ({ name }) => {
      if (name === 'size/S') throw Object.assign(new Error('Label does not exist'), { status: 404 });
      return { data: [] };
    },
  });

  const { log } = await runSizeStep(github, pr);
  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.addLabels', params: { ...REPO, issue_number: NUMBER, labels: ['size/M'] } },
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: NUMBER, name: 'size/S' } },
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: NUMBER, name: 'size/XL' } },
  ]);
  assert.deepEqual(log.info, ['100 lines changed, added size/M', 'size/S was already removed', 'removed size/XL']);
});

test('a failed size label removal other than a 404 fails the job', async () => {
  const pr = { additions: 100, deletions: 0, labels: ['size/S'] };
  const { github, calls } = fakeGithub(pr, {
    'rest.issues.removeLabel': () => {
      throw Object.assign(new Error('Server Error'), { status: 500 });
    },
  });

  await assert.rejects(runSizeStep(github, pr), { status: 500, message: 'Server Error' });
  assert.deepEqual(writes(calls).map(({ route }) => route), ['rest.issues.addLabels', 'rest.issues.removeLabel']);
});
