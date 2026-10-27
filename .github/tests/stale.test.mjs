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

// Tests for .github/workflows/stale.yaml: the idle pull request nudges, the
// lifecycle/stale label on pull requests, the lock of old closed threads, and
// the actions/stale inputs for idle questions. The scripts run against a
// recorded fake of the GitHub API, with Date pinned by node:test mock timers.
//
// Fixture shapes follow real responses from dsx-ai-factory/topograph:
//   - Timeline events carry `actor` and `created_at`, except `reviewed`
//     (`user` and `submitted_at`, no actor) and `committed` (no actor or
//     user, only `committer.date`). `commented` carries both actor and user.
//   - A comment that @-mentions someone, including one from
//     github-actions[bot], is followed a second or two later by `mentioned`
//     and `subscribed` events whose actor is the mentioned user (issue #263).
//   - pulls.listReviews returns reviews oldest first, with upper-case states.
//   - issues.listForRepo returns pull requests too, marked by `pull_request`.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';

import { GITHUB_DIR, extractStep, fakeCore, loadWorkflow, recordingGithub, runGithubScript } from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'stale.yaml');
const REPO = { owner: 'dsx-ai-factory', repo: 'topograph' };
const GUARD = "github.repository == 'dsx-ai-factory/topograph'";
const ACTIONS_BOT = 'github-actions[bot]';

// Friday. Most nudge tests run at this time.
const FRIDAY = '2026-10-23T10:00:00Z';

const WRITE_ROUTES = new Set([
  'rest.issues.createComment',
  'rest.issues.addLabels',
  'rest.issues.removeLabel',
  'rest.issues.lock',
  'rest.issues.update',
  'rest.pulls.update',
]);

function account(login, type = 'User') {
  return { login, type };
}

function bot(login) {
  return account(login, 'Bot');
}

function githubTime(ms) {
  return new Date(ms).toISOString().replace('.000Z', 'Z');
}

// Timeline events.
function commented(who, at, body = 'Looks good to me.') {
  return { event: 'commented', actor: who, user: who, created_at: at, body };
}

function reviewed(who, at, state) {
  return { event: 'reviewed', user: who, submitted_at: at, state };
}

function committed(at) {
  const person = { name: 'A Contributor', email: 'contributor@example.com', date: at };
  return { event: 'committed', sha: '0a1b2c3', author: person, committer: person, message: 'Address review' };
}

function acted(event, who, at, extra = {}) {
  return { event, actor: who, created_at: at, ...extra };
}

// One entry of pulls.listReviews.
function review(login, state, submittedAt) {
  return { user: account(login), state, submitted_at: submittedAt };
}

// One open pull request as pulls.list returns it.
function pull(number, { login = `author${number}`, draft = false, requested = [], labels = [], created = '2026-08-01T09:00:00Z' } = {}) {
  return {
    number,
    draft,
    user: account(login),
    created_at: created,
    requested_reviewers: requested.map((reviewer) => account(reviewer)),
    requested_teams: [],
    labels: labels.map((name) => ({ name })),
  };
}

function page(items, { page: pageNumber = 1, per_page: perPage = 30 }) {
  return { data: items.slice((pageNumber - 1) * perPage, pageNumber * perPage) };
}

// A fake repository holding open pull requests, their timelines and their
// reviews. Comments and label changes are written back the way GitHub records
// them, so a second run sees what the first one did.
function fakeRepo({ pulls, timelines = {}, reviews = {} }, extraRoutes = {}) {
  const timeline = (number) => (timelines[number] ??= []);
  const pr = (number) => pulls.find((p) => p.number === number);
  return recordingGithub({
    'rest.pulls.list': (params) => page(pulls, params),
    'rest.issues.listEventsForTimeline': (params) => page(timeline(params.issue_number), params),
    'rest.pulls.listReviews': (params) => page(reviews[params.pull_number] ?? [], params),
    'rest.issues.createComment': ({ issue_number: number, body }) => {
      const now = Date.now();
      timeline(number).push(commented(bot(ACTIONS_BOT), githubTime(now), body));
      for (const [, login] of body.matchAll(/@([A-Za-z0-9-]+)/g)) {
        timeline(number).push(acted('mentioned', account(login), githubTime(now + 1000)));
        timeline(number).push(acted('subscribed', account(login), githubTime(now + 2000)));
      }
      return { data: { id: number } };
    },
    'rest.issues.addLabels': ({ issue_number: number, labels }) => {
      for (const name of labels) {
        pr(number).labels.push({ name });
        timeline(number).push(acted('labeled', bot(ACTIONS_BOT), githubTime(Date.now()), { label: { name } }));
      }
      return { data: pr(number).labels };
    },
    'rest.issues.removeLabel': ({ issue_number: number, name }) => {
      pr(number).labels = pr(number).labels.filter((label) => label.name !== name);
      timeline(number).push(acted('unlabeled', bot(ACTIONS_BOT), githubTime(Date.now()), { label: { name } }));
      return { data: pr(number).labels };
    },
    ...extraRoutes,
  });
}

function clock(t, iso) {
  t.mock.timers.enable({ apis: ['Date'], now: Date.parse(iso) });
}

async function runStep(github, jobId, stepId) {
  const { script } = extractStep(WORKFLOW, jobId, stepId);
  const { core, log } = fakeCore();
  await runGithubScript(script, { github, context: { repo: REPO }, core });
  return log;
}

const runNudge = (github) => runStep(github, 'pull-requests', 'nudge');
const runLock = (github) => runStep(github, 'lock', 'lock');

function writes(calls) {
  return calls.filter(({ route }) => WRITE_ROUTES.has(route));
}

function comments(calls) {
  return calls.filter(({ route }) => route === 'rest.issues.createComment').map(({ params }) => params);
}

function marker(iso) {
  return `<!-- topograph-idle-nudge:${iso} -->`;
}

test('changes requested at 14 days nudges the author', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [
      pull(21, { login: 'alice', requested: ['ravisoundar'] }),
      // A second short of 14 days: no nudge, and its 9 business days must
      // not route it to the reviewers either.
      pull(22, { login: 'erin', requested: ['ravisoundar'] }),
    ],
    timelines: {
      21: [
        acted('review_requested', account('alice'), '2026-10-08T08:00:00Z'),
        reviewed(account('dmitsh'), '2026-10-08T09:00:00Z', 'commented'),
        reviewed(account('dmitsh'), '2026-10-09T10:00:00Z', 'changes_requested'),
      ],
      22: [reviewed(account('dmitsh'), '2026-10-09T10:00:01Z', 'changes_requested')],
    },
    reviews: {
      21: [review('dmitsh', 'COMMENTED', '2026-10-08T09:00:00Z'), review('dmitsh', 'CHANGES_REQUESTED', '2026-10-09T10:00:00Z')],
      22: [review('dmitsh', 'CHANGES_REQUESTED', '2026-10-09T10:00:01Z')],
    },
  });

  await runNudge(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [21]);
  const { body } = posted[0];
  assert.ok(body.startsWith('@alice a reviewer requested changes on this pull request'), body);
  assert.ok(body.includes('no activity for 14 days'), body);
  assert.ok(!body.includes('@ravisoundar') && !body.includes('@dmitsh'), body);
  assert.ok(body.endsWith(marker('2026-10-09T10:00:00.000Z')), body);
});

test('awaiting review at 5 business days across a weekend nudges the reviewer', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(31, { login: 'bob', requested: ['dmitsh'] })],
    timelines: {
      31: [
        reviewed(account('ravisoundar'), '2026-10-14T10:00:00Z', 'changes_requested'),
        reviewed(account('ravisoundar'), '2026-10-16T09:00:00Z', 'approved'),
        commented(account('bob'), '2026-10-16T10:00:00Z', 'Thanks, rebased.'),
      ],
    },
    // The changes request was followed by an approval from the same
    // reviewer, so nothing is waiting on the author.
    reviews: {
      31: [review('ravisoundar', 'CHANGES_REQUESTED', '2026-10-14T10:00:00Z'), review('ravisoundar', 'APPROVED', '2026-10-16T09:00:00Z')],
    },
  });

  await runNudge(github);

  assert.deepEqual(writes(calls), [
    {
      route: 'rest.issues.createComment',
      params: {
        ...REPO,
        issue_number: 31,
        body:
          '@dmitsh this pull request has waited 5 business days for a review. ' +
          'Please review it, or reply here with when you expect to.\n\n' +
          'These reminders and the `lifecycle/stale` label are explained under ' +
          '[Following up](https://github.com/dsx-ai-factory/topograph/blob/main/CONTRIBUTING.md#following-up). ' +
          'Pull requests are never closed automatically.\n\n' +
          '<!-- topograph-idle-nudge:2026-10-16T10:00:00.000Z -->',
      },
    },
  ]);
});

test('awaiting review at 4 business days does not nudge', async (t) => {
  // Friday to Thursday: Monday to Thursday count, the weekend does not.
  clock(t, '2026-10-22T10:00:00Z');
  const { github, calls } = fakeRepo({
    pulls: [pull(32, { login: 'bob', requested: ['dmitsh'] })],
    timelines: { 32: [commented(account('bob'), '2026-10-16T10:00:00Z')] },
  });

  await runNudge(github);

  assert.deepEqual(writes(calls), []);
});

test('awaiting review with no requested reviewer logs and does not comment', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(33, { login: 'bob' })],
    timelines: { 33: [commented(account('bob'), '2026-10-16T10:00:00Z')] },
  });

  const log = await runNudge(github);

  assert.deepEqual(writes(calls), []);
  assert.ok(
    log.info.includes('#33: waiting 5 business days for review, but no reviewer is requested; no comment'),
    log.info.join('\n'),
  );
});

test('draft nudges the author only', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [
      pull(41, { login: 'carol', draft: true, requested: ['ravisoundar'] }),
      // 10 days and 8 business days: past the reviewer threshold, but a
      // draft never pages its reviewers.
      pull(42, { login: 'dave', draft: true, requested: ['ravisoundar'] }),
    ],
    timelines: {
      41: [commented(account('carol'), '2026-10-09T10:00:00Z', 'Still working on this.')],
      42: [commented(account('dave'), '2026-10-13T10:00:00Z', 'Still working on this.')],
    },
  });

  await runNudge(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [41]);
  const { body } = posted[0];
  assert.ok(body.startsWith('@carol this draft pull request has had no activity for 14 days'), body);
  assert.ok(!body.includes('@ravisoundar'), body);
  assert.ok(body.endsWith(marker('2026-10-09T10:00:00.000Z')), body);
});

test('61 days idle adds lifecycle/stale and never closes', async (t) => {
  clock(t, FRIDAY);
  // Recorded rather than rejected, so a close attempt shows up in calls.
  const { github, calls } = fakeRepo(
    {
      pulls: [pull(51, { login: 'frank', requested: ['dmitsh'] })],
      timelines: { 51: [commented(account('frank'), '2026-08-23T10:00:00Z')] },
    },
    { 'rest.pulls.update': () => ({ data: {} }), 'rest.issues.update': () => ({ data: {} }) },
  );

  await runNudge(github);

  const changed = writes(calls);
  assert.deepEqual(changed.map(({ route, params }) => [route, params.issue_number]), [
    ['rest.issues.createComment', 51],
    ['rest.issues.addLabels', 51],
  ]);
  assert.ok(changed[0].params.body.startsWith('@dmitsh '), changed[0].params.body);
  assert.ok(changed[0].params.body.endsWith(marker('2026-08-23T10:00:00.000Z')), changed[0].params.body);
  assert.deepEqual(changed[1].params, { ...REPO, issue_number: 51, labels: ['lifecycle/stale'] });
  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.pulls.update' || route === 'rest.issues.update'),
    [],
  );
});

test('lifecycle/stale is added at 30 days, not at 29', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(53), pull(54)],
    timelines: {
      53: [commented(account('author53'), '2026-09-23T10:00:00Z')],
      54: [commented(account('author54'), '2026-09-23T10:00:01Z')],
    },
  });

  await runNudge(github);

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.addLabels', params: { ...REPO, issue_number: 53, labels: ['lifecycle/stale'] } },
  ]);
});

test('fresh activity removes lifecycle/stale', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(61, { login: 'gina', requested: ['dmitsh'], labels: ['lifecycle/stale'] })],
    timelines: {
      61: [
        acted('review_requested', account('gina'), '2026-09-01T10:00:00Z'),
        acted('labeled', bot(ACTIONS_BOT), '2026-10-01T06:20:00Z', { label: { name: 'lifecycle/stale' } }),
        // A push: the only fresh event, and it has no actor.
        committed('2026-10-21T10:00:00Z'),
      ],
    },
  });

  await runNudge(github);

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: 61, name: 'lifecycle/stale' } },
  ]);
});

test('second run does not repeat the nudge', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(71, { login: 'hank', requested: ['dmitsh'] })],
    timelines: { 71: [commented(account('hank'), '2026-10-16T10:00:00Z')] },
  });

  await runNudge(github);
  assert.deepEqual(comments(calls).map((c) => c.issue_number), [71]);

  // The next scheduled run. Saturday is not a business day, so the PR is
  // still 5 business days idle and the nudge is still due.
  t.mock.timers.setTime(Date.parse('2026-10-24T06:15:00Z'));
  await runNudge(github);

  assert.deepEqual(writes(calls).map(({ route, params }) => [route, params.issue_number]), [
    ['rest.issues.createComment', 71],
  ]);
});

test('marker from a non-bot user is ignored', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(81, { login: 'ivy', requested: ['dmitsh'] }), pull(82, { login: 'jack', requested: ['dmitsh'] })],
    timelines: {
      // A comment edited after posting to carry the marker for its own
      // timestamp; an edit adds no timeline event.
      81: [commented(account('mallory'), '2026-10-16T10:00:00Z', `LGTM ${marker('2026-10-16T10:00:00.000Z')}`)],
      // Another bot is not github-actions[bot].
      82: [
        commented(account('jack'), '2026-10-16T10:00:00Z'),
        commented(bot('coderabbitai[bot]'), '2026-10-16T10:05:00Z', `Summary ${marker('2026-10-16T10:00:00.000Z')}`),
      ],
    },
  });

  await runNudge(github);

  assert.deepEqual(comments(calls).map((c) => c.issue_number), [81, 82]);
});

test('bot activity does not reset idleness', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(91, { login: 'kim', requested: ['dmitsh'] })],
    timelines: {
      91: [
        commented(account('kim'), '2026-10-16T10:00:00Z'),
        commented(bot('coderabbitai[bot]'), '2026-10-22T10:00:00Z'),
        // A review carries `user` and no `actor`.
        reviewed(bot('greptile-apps[bot]'), '2026-10-22T11:00:00Z', 'commented'),
        acted('labeled', bot(ACTIONS_BOT), '2026-10-22T12:00:00Z', { label: { name: 'area/ci' } }),
      ],
    },
    reviews: { 91: [{ user: bot('greptile-apps[bot]'), state: 'COMMENTED', submitted_at: '2026-10-22T11:00:00Z' }] },
  });

  await runNudge(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [91]);
  assert.ok(posted[0].body.endsWith(marker('2026-10-16T10:00:00.000Z')), posted[0].body);
});

test('mention events from a nudge do not reset idleness', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(141, { login: 'lee', requested: ['dmitsh'] })],
    timelines: {
      141: [
        commented(account('lee'), '2026-09-18T10:00:00Z'),
        commented(bot(ACTIONS_BOT), '2026-09-25T06:20:00Z', `@dmitsh please review.\n\n${marker('2026-09-18T10:00:00.000Z')}`),
        acted('mentioned', account('dmitsh'), '2026-09-25T06:20:01Z'),
        acted('subscribed', account('dmitsh'), '2026-09-25T06:20:02Z'),
      ],
    },
  });

  await runNudge(github);

  // Idle since 2026-09-18: 35 days, so stale; the nudge for that period
  // already went out.
  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.addLabels', params: { ...REPO, issue_number: 141, labels: ['lifecycle/stale'] } },
  ]);
});

test('a new pull request with older commits is not idle', async (t) => {
  clock(t, FRIDAY);
  // Opened yesterday from a branch whose only commit is a month old.
  const { github, calls } = fakeRepo({
    pulls: [pull(161, { login: 'nia', requested: ['dmitsh'], created: '2026-10-22T09:00:00Z' })],
    timelines: { 161: [committed('2026-09-20T10:00:00Z')] },
  });

  await runNudge(github);

  assert.deepEqual(writes(calls), []);
});

test('exempt label is skipped', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [
      pull(101, { requested: ['dmitsh'], labels: ['lifecycle/frozen'] }),
      pull(102, { login: 'olga', draft: true, labels: ['do-not-merge'] }),
    ],
    timelines: {
      101: [commented(account('author101'), '2026-08-23T10:00:00Z')],
      102: [commented(account('olga'), '2026-08-23T10:00:00Z')],
    },
  });

  await runNudge(github);

  assert.deepEqual(writes(calls), []);
});

test('activity past the first timeline page counts', async (t) => {
  clock(t, FRIDAY);
  const events = Array.from({ length: 130 }, (_, i) => commented(account('pat'), githubTime(Date.parse('2026-08-01T00:00:00Z') + i * 60_000)));
  events.push(commented(account('pat'), '2026-10-22T10:00:00Z'));
  const { github, calls } = fakeRepo({ pulls: [pull(171, { login: 'pat', requested: ['dmitsh'] })], timelines: { 171: events } });

  await runNudge(github);

  assert.deepEqual(writes(calls), []);
});

test('second page of open PRs is processed', async (t) => {
  clock(t, FRIDAY);
  const pulls = [];
  const timelines = {};
  for (let number = 1; number <= 150; number += 1) {
    const idle = number === 150;
    pulls.push(pull(number, { requested: idle ? ['dmitsh'] : [] }));
    timelines[number] = [commented(account(`author${number}`), idle ? '2026-10-16T10:00:00Z' : '2026-10-22T10:00:00Z')];
  }
  const { github, calls } = fakeRepo({ pulls, timelines });

  await runNudge(github);

  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.pulls.list').map(({ params }) => params),
    [
      { ...REPO, state: 'open', per_page: 100, page: 1 },
      { ...REPO, state: 'open', per_page: 100, page: 2 },
    ],
  );
  assert.deepEqual(writes(calls).map(({ route, params }) => [route, params.issue_number]), [
    ['rest.issues.createComment', 150],
  ]);
});

// A fake repository of closed issues and pull requests for the lock job.
// issues.listForRepo sorts the current state on every call, and a comment or
// a lock moves the item's updated_at to now, as on GitHub.
function closedRepo(items) {
  const state = items.map((item) => ({ ...item }));
  const find = (number) => state.find((item) => item.number === number);
  return recordingGithub({
    'rest.issues.listForRepo': (params) => {
      const sorted = [...state].sort((a, b) => a.updated_at.localeCompare(b.updated_at) || a.number - b.number);
      return page(sorted.map((item) => ({ ...item })), params);
    },
    'rest.issues.createComment': ({ issue_number: number }) => {
      find(number).updated_at = githubTime(Date.now());
      return { data: { id: number } };
    },
    'rest.issues.lock': ({ issue_number: number }) => {
      Object.assign(find(number), { locked: true, active_lock_reason: 'resolved', updated_at: githubTime(Date.now()) });
      return { status: 204, data: '' };
    },
  });
}

function closed(number, updatedAt, { locked = false, isPull = number % 2 === 0 } = {}) {
  const item = {
    number,
    state: 'closed',
    locked,
    active_lock_reason: locked ? 'resolved' : null,
    updated_at: updatedAt,
    closed_at: updatedAt,
  };
  if (isPull) item.pull_request = { url: `https://api.github.com/repos/dsx-ai-factory/topograph/pulls/${number}` };
  return item;
}

test('lock locks 91-day closed items and stops at the first recent one', async (t) => {
  clock(t, FRIDAY);
  const items = [];
  for (let number = 1; number <= 150; number += 1) items.push(closed(number, '2026-07-24T10:00:00Z'));
  // Enough recent items to fill page 2 and spill onto page 3, so reading
  // on past the first recent item would show as a third list call.
  for (let number = 151; number <= 210; number += 1) items.push(closed(number, '2026-07-26T10:00:00Z'));
  const { github, calls } = closedRepo(items);

  await runLock(github);

  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.issues.listForRepo').map(({ params }) => params),
    [
      { ...REPO, state: 'closed', sort: 'updated', direction: 'asc', per_page: 100, page: 1 },
      { ...REPO, state: 'closed', sort: 'updated', direction: 'asc', per_page: 100, page: 2 },
    ],
  );
  const expected = [];
  for (let number = 1; number <= 150; number += 1) {
    expected.push(['rest.issues.createComment', number], ['rest.issues.lock', number]);
  }
  const changed = writes(calls);
  assert.deepEqual(changed.map(({ route, params }) => [route, params.issue_number]), expected);
  for (const { route, params } of changed) {
    if (route === 'rest.issues.lock') assert.deepEqual(params, { ...REPO, issue_number: params.issue_number, lock_reason: 'resolved' });
  }
  const bodies = new Set(changed.filter(({ route }) => route === 'rest.issues.createComment').map(({ params }) => params.body));
  assert.equal(bodies.size, 1);
  const [body] = bodies;
  assert.ok(!body.includes('\n') && body.includes('open a new issue'), body);
});

test('lock skips already locked items', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = closedRepo([
    closed(1, '2026-06-25T10:00:00Z', { locked: true }),
    closed(2, '2026-07-15T10:00:00Z'),
    closed(3, '2026-10-13T10:00:00Z'),
  ]);

  await runLock(github);

  assert.equal(calls.filter(({ route }) => route === 'rest.issues.listForRepo').length, 1);
  assert.deepEqual(writes(calls).map(({ route, params }) => [route, params.issue_number]), [
    ['rest.issues.createComment', 2],
    ['rest.issues.lock', 2],
  ]);
});

test('questions job runs actions/stale as a dry run on question issues only', () => {
  const step = extractStep(WORKFLOW, 'questions', 'stale');
  assert.equal(step.uses, 'actions/stale@4391f3da665fdf50b6810c1a66712fb9ba21aa93');
  const { 'stale-issue-message': staleMessage, 'close-issue-message': closeMessage, ...inputs } = step.with;
  assert.deepEqual(inputs, {
    'only-issue-labels': 'question',
    'days-before-issue-stale': 90,
    'days-before-issue-close': 30,
    'days-before-pr-stale': -1,
    'days-before-pr-close': -1,
    'stale-issue-label': 'lifecycle/stale',
    'exempt-issue-labels': 'lifecycle/frozen',
    'debug-only': true,
    'operations-per-run': 100,
  });
  // How to keep the question open, and how to come back after it closes.
  assert.match(staleMessage, /\bcomment\b/i);
  assert.match(closeMessage, /\breopen\b/i);
});

test('runs daily and on demand, upstream only, with only the permissions each job uses', () => {
  const wf = loadWorkflow(WORKFLOW);
  assert.deepEqual(wf.on, { schedule: [{ cron: '15 6 * * *' }], workflow_dispatch: null });
  // A manual run that overlapped the scheduled one would see no reminder
  // yet and post a second one, so runs queue and are never cancelled.
  assert.deepEqual(wf.concurrency, { group: '${{ github.workflow }}', 'cancel-in-progress': false });
  assert.deepEqual(wf.permissions, {});
  const jobs = Object.fromEntries(Object.entries(wf.jobs).map(([id, job]) => [id, { if: job.if, permissions: job.permissions }]));
  assert.deepEqual(jobs, {
    questions: { if: GUARD, permissions: { issues: 'write' } },
    'pull-requests': { if: GUARD, permissions: { 'pull-requests': 'write' } },
    lock: { if: GUARD, permissions: { issues: 'write', 'pull-requests': 'write' } },
  });
});
