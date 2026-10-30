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
// script has to read it from pulls.get. The fake keeps its state in the PR
// fixtures, so a second run against the same fixtures sees what the first
// one wrote.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';

import { GITHUB_DIR, extractStep, fakeCore, loadWorkflow, recordingGithub, runGithubScript } from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'conflict-check.yaml');
const REPO = { owner: 'dsx-ai-factory', repo: 'topograph' };
// The hidden marker the needs-rebase comment carries.
const MARKER = '<!-- conflict-check: needs-rebase -->';

// GitHub's clock for what the fake writes: one second per write, later than
// every timestamp a fixture sets by hand.
let seconds = 0;
function stamp() {
  seconds += 1;
  return new Date(Date.UTC(2026, 9, 30, 12, 0, seconds)).toISOString();
}

// One open pull request. `mergeable` is what pulls.get returns; an array is
// what successive reads return, the last value repeating. `comments` and
// `events` are what issues.listComments and issues.listEvents return.
function pr(
  number,
  { mergeable, draft = false, labels = [], login = `author${number}`, base = 'main', comments = [], events = [] },
) {
  return {
    number,
    draft,
    mergeable,
    base: { ref: base },
    labels: labels.map((name) => ({ name })),
    user: { login },
    comments,
    events,
  };
}

function page(items, { page: number = 1, per_page: perPage = 30 }) {
  return items.slice((number - 1) * perPage, number * perPage);
}

// A fake GitHub holding `prs`. pulls.list serves the open PRs whose base is
// `base` a page at a time, without `mergeable`; pulls.get serves each one in
// full. Label and comment writes change the fixtures and record an event.
// `routes` replaces individual handlers.
function fakeGithub(prs, routes = {}) {
  const reads = new Map();
  const find = (number) => prs.find((p) => p.number === number);
  return recordingGithub({
    'rest.pulls.list': (params) => ({
      data: page(
        prs.filter((p) => params.base === undefined || p.base.ref === params.base),
        params,
      ).map(({ number, draft, base, labels, user }) => ({ number, draft, base, labels, user })),
    }),
    'rest.pulls.get': ({ pull_number: number }) => {
      const p = find(number);
      const read = reads.get(number) ?? 0;
      reads.set(number, read + 1);
      const values = Array.isArray(p.mergeable) ? p.mergeable : [p.mergeable];
      const { comments, events, ...full } = p;
      return { data: { ...full, labels: [...p.labels], mergeable: values[Math.min(read, values.length - 1)] } };
    },
    'rest.issues.addLabels': ({ issue_number: number, labels }) => {
      const p = find(number);
      for (const name of labels) {
        if (!p.labels.some((label) => label.name === name)) p.labels.push({ name });
        p.events.push({ event: 'labeled', label: { name }, created_at: stamp() });
      }
      return { data: p.labels };
    },
    'rest.issues.removeLabel': ({ issue_number: number, name }) => {
      const p = find(number);
      p.labels = p.labels.filter((label) => label.name !== name);
      p.events.push({ event: 'unlabeled', label: { name }, created_at: stamp() });
      return { data: p.labels };
    },
    'rest.issues.createComment': ({ issue_number: number, body }) => {
      const p = find(number);
      p.comments.push({ body, created_at: stamp(), user: { login: 'github-actions[bot]' } });
      return { data: { id: p.comments.length } };
    },
    'rest.issues.listComments': (params) => ({ data: page(find(params.issue_number).comments, params) }),
    'rest.issues.listEvents': (params) => ({ data: page(find(params.issue_number).events, params) }),
    ...routes,
  });
}

// Runs the script with a fake setTimeout that records each delay and fires
// at once, so no test waits for real.
async function run(github) {
  const { script } = extractStep(WORKFLOW, 'conflicts', 'check');
  const { core, log } = fakeCore();
  const delays = [];
  const realSetTimeout = globalThis.setTimeout;
  globalThis.setTimeout = (callback, ms, ...args) => {
    delays.push(ms);
    queueMicrotask(() => callback(...args));
    return 0;
  };
  try {
    await runGithubScript(script, { github, context: { repo: REPO }, core });
  } finally {
    globalThis.setTimeout = realSetTimeout;
  }
  return { log, delays };
}

// The calls that change a pull request: labels and comments.
function writes(calls) {
  return calls.filter(({ route }) => route.startsWith('rest.issues.') && !route.startsWith('rest.issues.list'));
}

// The pull request numbers the script read with pulls.get, in order.
function fetched(calls) {
  return calls.filter(({ route }) => route === 'rest.pulls.get').map(({ params }) => params.pull_number);
}

function failure(status, message) {
  return () => {
    throw Object.assign(new Error(message), { status });
  };
}

test('conflicted PR gets label and one comment', async () => {
  const { github, calls } = fakeGithub([pr(7, { mergeable: false, login: 'octocat' })]);

  const { delays } = await run(github);

  // A PR that was not labeled needs no comment lookup, and a known
  // mergeable value needs no wait.
  assert.deepEqual(calls.map(({ route }) => route), [
    'rest.pulls.list',
    'rest.pulls.get',
    'rest.issues.addLabels',
    'rest.issues.createComment',
  ]);
  assert.deepEqual(delays, []);
  const changed = writes(calls);
  assert.deepEqual(changed[0].params, { ...REPO, issue_number: 7, labels: ['needs-rebase'] });
  const { body, ...target } = changed[1].params;
  assert.deepEqual(target, { ...REPO, issue_number: 7 });
  assert.ok(body.startsWith('@octocat '), `comment starts ${JSON.stringify(body.slice(0, 20))}`);
  assert.ok(body.includes('`main`'), 'comment does not name `main`');
  assert.ok(body.endsWith(`\n\n${MARKER}`), `comment ends ${JSON.stringify(body.slice(-50))}`);
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

test('unknown mergeable is read three times, then left alone', async () => {
  // One unlabeled and one labeled: treating null as a conflict would label
  // #9, and treating it as clean would strip the label from #10.
  const { github, calls } = fakeGithub([
    pr(9, { mergeable: null }),
    pr(10, { mergeable: null, labels: ['needs-rebase'] }),
  ]);

  const { log, delays } = await run(github);

  assert.deepEqual(writes(calls), []);
  assert.deepEqual(fetched(calls), [9, 10, 9, 10, 9, 10]);
  assert.deepEqual(delays, [5000, 10000]);
  assert.deepEqual(log.info, [
    '#9: mergeability still unknown after 3 reads, left for the next run',
    '#10: mergeability still unknown after 3 reads, left for the next run',
  ]);
});

test('mergeability computed on a later read is acted on', async () => {
  const { github, calls } = fakeGithub([
    pr(17, { mergeable: [null, false], login: 'octocat' }),
    pr(18, { mergeable: [null, null, true], labels: ['needs-rebase'] }),
  ]);

  const { delays } = await run(github);

  assert.deepEqual(fetched(calls), [17, 18, 17, 18, 18]);
  assert.deepEqual(delays, [5000, 10000]);
  assert.deepEqual(
    writes(calls).map(({ route, params }) => [route, params.issue_number]),
    [
      ['rest.issues.addLabels', 17],
      ['rest.issues.createComment', 17],
      ['rest.issues.removeLabel', 18],
    ],
  );
});

test('no further wait once every mergeability is known', async () => {
  const { github, calls } = fakeGithub([pr(19, { mergeable: [null, true], labels: ['needs-rebase'] })]);

  const { delays } = await run(github);

  assert.deepEqual(fetched(calls), [19, 19]);
  assert.deepEqual(delays, [5000]);
  assert.deepEqual(writes(calls).map(({ route }) => route), ['rest.issues.removeLabel']);
});

test('already labeled conflicted PR gets no second comment', async () => {
  // The marker comment came after needs-rebase was added. The later `bug`
  // label is not a new conflict.
  const { github, calls } = fakeGithub([
    pr(11, {
      mergeable: false,
      labels: ['needs-rebase', 'bug'],
      events: [
        { event: 'labeled', label: { name: 'needs-rebase' }, created_at: '2026-10-20T00:00:00Z' },
        { event: 'labeled', label: { name: 'bug' }, created_at: '2026-10-22T00:00:00Z' },
      ],
      comments: [
        { body: `@author11 please rebase.\n\n${MARKER}`, created_at: '2026-10-20T00:00:01Z', user: { login: 'github-actions[bot]' } },
      ],
    }),
  ]);

  await run(github);

  assert.deepEqual(writes(calls), []);
  assert.deepEqual(fetched(calls), [11]);
});

test('a failed comment is posted by the next run, and only once', async () => {
  const prs = [pr(15, { mergeable: false, login: 'octocat' })];

  const first = fakeGithub(prs, { 'rest.issues.createComment': failure(502, 'Server Error') });
  await assert.rejects(run(first.github), { status: 502, message: 'Server Error' });
  assert.deepEqual(writes(first.calls).map(({ route }) => route), ['rest.issues.addLabels', 'rest.issues.createComment']);

  // The PR now has needs-rebase and no comment.
  const second = fakeGithub(prs);
  await run(second.github);
  const posted = writes(second.calls);
  assert.deepEqual(posted.map(({ route, params }) => [route, params.issue_number]), [['rest.issues.createComment', 15]]);
  assert.ok(posted[0].params.body.startsWith('@octocat '), 'retried comment does not mention the author');
  assert.ok(posted[0].params.body.endsWith(MARKER), 'retried comment has no marker');

  const third = fakeGithub(prs);
  await run(third.github);
  assert.deepEqual(writes(third.calls), []);
});

test('a comment from an earlier conflict does not count for the current one', async () => {
  // #16 conflicted, was told, merged cleanly, and conflicts again; the
  // comment for the second conflict failed after the label went on.
  const { github, calls } = fakeGithub([
    pr(16, {
      mergeable: false,
      labels: ['needs-rebase'],
      events: [
        { event: 'labeled', label: { name: 'needs-rebase' }, created_at: '2026-10-01T00:00:00Z' },
        { event: 'unlabeled', label: { name: 'needs-rebase' }, created_at: '2026-10-05T00:00:00Z' },
        { event: 'labeled', label: { name: 'needs-rebase' }, created_at: '2026-10-20T00:00:00Z' },
      ],
      comments: [
        { body: `@author16 please rebase.\n\n${MARKER}`, created_at: '2026-10-01T00:00:01Z', user: { login: 'github-actions[bot]' } },
        { body: 'Rebased, thanks.', created_at: '2026-10-21T00:00:00Z', user: { login: 'author16' } },
      ],
    }),
  ]);

  await run(github);

  assert.deepEqual(writes(calls).map(({ route, params }) => [route, params.issue_number]), [['rest.issues.createComment', 16]]);
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

test('PRs that target another branch are not checked', async () => {
  // #20 conflicts with its own base, not with main; reading it would tell
  // its author to update from main.
  const { github, calls } = fakeGithub([
    pr(20, { mergeable: false, base: 'release-1.1' }),
    pr(21, { mergeable: false }),
  ]);

  await run(github);

  assert.deepEqual(fetched(calls), [21]);
  assert.deepEqual(
    writes(calls).map(({ route, params }) => [route, params.issue_number]),
    [
      ['rest.issues.addLabels', 21],
      ['rest.issues.createComment', 21],
    ],
  );
});

test('second page of open PRs is processed', async () => {
  const prs = Array.from({ length: 150 }, (_, i) => pr(i + 1, { mergeable: i + 1 !== 150 }));
  const { github, calls } = fakeGithub(prs);

  await run(github);

  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.pulls.list').map(({ params }) => params),
    [
      { ...REPO, state: 'open', base: 'main', per_page: 100, page: 1 },
      { ...REPO, state: 'open', base: 'main', per_page: 100, page: 2 },
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
    'rest.issues.addLabels': failure(403, 'You do not have permission to create labels on this repository.'),
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
