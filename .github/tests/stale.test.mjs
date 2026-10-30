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
//   - A commit's date is when it was made, not when it was pushed: on #643
//     the head commit is dated 10:59:10 and was pushed at 11:00:01. Commits
//     by bots carry a git identity only, such as `dependabot[bot]` (#631) or
//     `github-actions[bot]` (#632, opened by publish-fern-docs.yml).
//   - actions.listWorkflowRunsForRepo with event=pull_request and head_sha
//     lists the runs a push to the pull request started for its head
//     commit; pr-title.yaml runs on every push. A run carries `actor`, the
//     account that pushed, and `created_at` (#643: ArangoGutierrez at
//     11:00:06). copy-pr-bot's runs for the same commit are `push` runs.
//   - A comment that @-mentions someone, including one from
//     github-actions[bot], is followed a second or two later by `mentioned`
//     and `subscribed` events whose actor is the mentioned user (issue #263).
//   - pulls.listReviews returns reviews oldest first, with upper-case states.
//     A reply in a review thread is a COMMENTED review. A dismissed review
//     keeps its id and shows as DISMISSED (PR #569), and the timeline adds a
//     `review_dismissed` event whose actor is the person who dismissed it.
//   - issues.listForRepo returns pull requests too, marked by `pull_request`.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync } from 'node:fs';
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

function committed(at, { author = { name: 'A Contributor', email: 'contributor@example.com' }, committer = author } = {}) {
  return {
    event: 'committed',
    sha: '0a1b2c3',
    author: { ...author, date: at },
    committer: { ...committer, date: at },
    message: 'Address review',
  };
}

const DEPENDABOT_GIT = { name: 'dependabot[bot]', email: '49699333+dependabot[bot]@users.noreply.github.com' };
const ACTIONS_GIT = { name: 'github-actions[bot]', email: '41898282+github-actions[bot]@users.noreply.github.com' };
const WEB_FLOW_GIT = { name: 'GitHub', email: 'noreply@github.com' };

// One workflow run as actions.listWorkflowRunsForRepo lists it. `sha`
// defaults to the pull request's head commit.
function workflowRun(who, at, { event = 'pull_request', sha } = {}) {
  return { event, actor: who, triggering_actor: who, created_at: at, head_sha: sha };
}

function acted(event, who, at, extra = {}) {
  return { event, actor: who, created_at: at, ...extra };
}

// One entry of pulls.listReviews.
function review(login, state, submittedAt) {
  return { user: account(login), state, submitted_at: submittedAt };
}

// One open pull request as pulls.list returns it.
function pull(
  number,
  { login = `author${number}`, draft = false, requested = [], teams = [], labels = [], created = '2026-08-01T09:00:00Z' } = {},
) {
  return {
    number,
    draft,
    user: account(login),
    created_at: created,
    head: { sha: `head${number}` },
    requested_reviewers: requested.map((reviewer) => account(reviewer)),
    requested_teams: teams.map((slug) => ({ name: slug, slug })),
    labels: labels.map((name) => ({ name })),
  };
}

function page(items, { page: pageNumber = 1, per_page: perPage = 30 }) {
  return { data: items.slice((pageNumber - 1) * perPage, pageNumber * perPage) };
}

// A fake repository holding open pull requests, their timelines, their
// reviews and the workflow runs on their commits. Comments and label changes
// are written back the way GitHub records them, so a second run sees what the
// first one did.
function fakeRepo({ pulls, timelines = {}, reviews = {}, runs = {} }, extraRoutes = {}) {
  const timeline = (number) => (timelines[number] ??= []);
  const pr = (number) => pulls.find((p) => p.number === number);
  const allRuns = () =>
    Object.entries(runs).flatMap(([number, list]) => list.map((run) => ({ ...run, head_sha: run.head_sha ?? pr(Number(number)).head.sha })));
  return recordingGithub({
    'rest.pulls.list': (params) => page(pulls, params),
    'rest.issues.listEventsForTimeline': (params) => page(timeline(params.issue_number), params),
    'rest.pulls.listReviews': (params) => page(reviews[params.pull_number] ?? [], params),
    // Filtered by head_sha and event as GitHub filters them. A page is the
    // list paginate hands back: it unwraps GitHub's { total_count,
    // workflow_runs } page into its items (plugin-paginate-rest,
    // normalizePaginatedListResponse).
    'rest.actions.listWorkflowRunsForRepo': (params) =>
      page(
        allRuns().filter((run) => run.head_sha === params.head_sha && (params.event === undefined || run.event === params.event)),
        params,
      ),
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

// Runs a script with its dry run switch `name` turned off. The switch must
// occur exactly once, so a renamed or reworded constant fails here instead of
// leaving the test on the shipped dry run.
async function runStepLive(github, jobId, stepId, name) {
  const { script } = extractStep(WORKFLOW, jobId, stepId);
  const shipped = `const ${name} = true;`;
  assert.equal(script.split(shipped).length - 1, 1, `the ${jobId} script must contain "${shipped}" exactly once`);
  const { core, log } = fakeCore();
  await runGithubScript(script.replace(shipped, `const ${name} = false;`), { github, context: { repo: REPO }, core });
  return log;
}

// Runs the nudge script as shipped, which is a dry run.
const runNudge = (github) => runStep(github, 'pull-requests', 'nudge');

// Runs the nudge script with its dry run switched off.
const runNudgeLive = (github) => runStepLive(github, 'pull-requests', 'nudge', 'NUDGE_DRY_RUN');

// Runs the lock script as shipped, which is a dry run.
const runLock = (github) => runStep(github, 'lock', 'lock');

// Runs the lock script with its dry run switched off.
const runLockLive = (github) => runStepLive(github, 'lock', 'lock', 'LOCK_DRY_RUN');

const FOOTER =
  'These reminders and the `lifecycle/stale` label are explained under ' +
  '[Following up](https://github.com/dsx-ai-factory/topograph/blob/main/CONTRIBUTING.md#following-up). ' +
  'Pull requests are never closed automatically.';

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

  await runNudgeLive(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [21]);
  const { body } = posted[0];
  assert.ok(body.startsWith('@alice a reviewer requested changes on this pull request'), body);
  assert.ok(body.includes('no activity for 14 days'), body);
  assert.ok(!body.includes('@ravisoundar') && !body.includes('@dmitsh'), body);
  assert.ok(body.endsWith(marker('2026-10-09T10:00:00.000Z')), body);
});

test('commented reply keeps changes requested', async (t) => {
  clock(t, FRIDAY);
  // dmitsh requested changes, then replied in a review thread, which is a
  // COMMENTED review. GitHub still lists the PR under
  // review:changes_requested in that case, so it waits on its author.
  const { github, calls } = fakeRepo({
    pulls: [pull(181, { login: 'quinn', requested: ['ravisoundar'] })],
    timelines: {
      181: [
        reviewed(account('dmitsh'), '2026-10-02T10:00:00Z', 'changes_requested'),
        reviewed(account('dmitsh'), '2026-10-09T10:00:00Z', 'commented'),
      ],
    },
    reviews: {
      181: [review('dmitsh', 'CHANGES_REQUESTED', '2026-10-02T10:00:00Z'), review('dmitsh', 'COMMENTED', '2026-10-09T10:00:00Z')],
    },
  });

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), [
    {
      route: 'rest.issues.createComment',
      params: {
        ...REPO,
        issue_number: 181,
        body:
          '@quinn a reviewer requested changes on this pull request, and it has had no activity for 14 days. ' +
          'Push an update or reply to the review when you are ready.\n\n' +
          `${FOOTER}\n\n` +
          '<!-- topograph-idle-nudge:2026-10-09T10:00:00.000Z -->',
      },
    },
  ]);
});

test('dismissed review clears changes requested', async (t) => {
  clock(t, FRIDAY);
  // dmitsh requested changes, and his later review was dismissed. The
  // dismissal is the newest activity, 5 business days ago, and nothing waits
  // on the author any more.
  const { github, calls } = fakeRepo({
    pulls: [pull(191, { login: 'rosa', requested: ['ravisoundar'] })],
    timelines: {
      191: [
        reviewed(account('dmitsh'), '2026-10-12T10:00:00Z', 'changes_requested'),
        reviewed(account('dmitsh'), '2026-10-14T10:00:00Z', 'dismissed'),
        acted('review_dismissed', account('dmitsh'), '2026-10-16T10:00:00Z', {
          dismissed_review: { state: 'changes_requested', review_id: 2, dismissal_message: 'Addressed in the last push.' },
        }),
      ],
    },
    reviews: {
      191: [review('dmitsh', 'CHANGES_REQUESTED', '2026-10-12T10:00:00Z'), review('dmitsh', 'DISMISSED', '2026-10-14T10:00:00Z')],
    },
  });

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), [
    {
      route: 'rest.issues.createComment',
      params: {
        ...REPO,
        issue_number: 191,
        body:
          '@ravisoundar this pull request has waited 5 business days for a review. ' +
          'Please review it, or reply here with when you expect to.\n\n' +
          `${FOOTER}\n\n` +
          '<!-- topograph-idle-nudge:2026-10-16T10:00:00.000Z -->',
      },
    },
  ]);
});

test('re-requesting a review from the reviewer who requested changes waits on that reviewer', async (t) => {
  clock(t, FRIDAY);
  // GitHub drops a reviewer from requested_reviewers once they review, so
  // dmitsh being listed after his changes request means the author asked him
  // to review again.
  const { github, calls } = fakeRepo({
    pulls: [
      pull(231, { login: 'zoe', requested: ['dmitsh'] }),
      // ravisoundar's request still stands, so this one waits on its author.
      pull(232, { login: 'abe', requested: ['dmitsh'] }),
    ],
    timelines: {
      231: [
        reviewed(account('dmitsh'), '2026-10-09T10:00:00Z', 'changes_requested'),
        acted('review_requested', account('zoe'), '2026-10-16T10:00:00Z', { requested_reviewer: account('dmitsh') }),
      ],
      232: [
        reviewed(account('ravisoundar'), '2026-10-08T10:00:00Z', 'changes_requested'),
        reviewed(account('dmitsh'), '2026-10-09T09:00:00Z', 'changes_requested'),
        acted('review_requested', account('abe'), '2026-10-09T10:00:00Z', { requested_reviewer: account('dmitsh') }),
      ],
    },
    reviews: {
      231: [review('dmitsh', 'CHANGES_REQUESTED', '2026-10-09T10:00:00Z')],
      232: [review('ravisoundar', 'CHANGES_REQUESTED', '2026-10-08T10:00:00Z'), review('dmitsh', 'CHANGES_REQUESTED', '2026-10-09T09:00:00Z')],
    },
  });

  await runNudgeLive(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [231, 232]);
  assert.ok(posted[0].body.startsWith('@dmitsh this pull request has waited 5 business days for a review.'), posted[0].body);
  assert.ok(
    posted[1].body.startsWith('@abe a reviewer requested changes on this pull request, and it has had no activity for 14 days.'),
    posted[1].body,
  );
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

  await runNudgeLive(github);

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

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), []);
});

test('awaiting review with no requested reviewer logs and does not comment', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(33, { login: 'bob' })],
    timelines: { 33: [commented(account('bob'), '2026-10-16T10:00:00Z')] },
  });

  const log = await runNudgeLive(github);

  assert.deepEqual(writes(calls), []);
  assert.ok(
    log.info.includes('#33: waiting 5 business days for review, but no reviewer is requested; no comment'),
    log.info.join('\n'),
  );
});

test('a review requested from a team reminds the team', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [
      pull(221, { login: 'xena', teams: ['topograph-reviewers'] }),
      pull(222, { login: 'yuri', requested: ['dmitsh'], teams: ['topograph-reviewers'] }),
    ],
    timelines: {
      221: [commented(account('xena'), '2026-10-16T10:00:00Z')],
      222: [commented(account('yuri'), '2026-10-16T10:00:00Z')],
    },
  });

  await runNudgeLive(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [221, 222]);
  assert.ok(
    posted[0].body.startsWith('@dsx-ai-factory/topograph-reviewers this pull request has waited 5 business days for a review.'),
    posted[0].body,
  );
  assert.ok(posted[1].body.startsWith('@dmitsh @dsx-ai-factory/topograph-reviewers this pull request has waited'), posted[1].body);
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

  await runNudgeLive(github);

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

  await runNudgeLive(github);

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

  await runNudgeLive(github);

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
        // A push: the only fresh event. The timeline holds its commit, which
        // has no actor; the run below is the push itself.
        committed('2026-10-21T10:00:00Z'),
      ],
    },
    runs: { 61: [workflowRun(account('gina'), '2026-10-21T10:00:05Z')] },
  });

  await runNudgeLive(github);

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

  await runNudgeLive(github);
  assert.deepEqual(comments(calls).map((c) => c.issue_number), [71]);

  // The next scheduled run. Saturday is not a business day, so the PR is
  // still 5 business days idle and the nudge is still due.
  t.mock.timers.setTime(Date.parse('2026-10-24T06:15:00Z'));
  await runNudgeLive(github);

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

  await runNudgeLive(github);

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

  await runNudgeLive(github);

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

  await runNudgeLive(github);

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

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), []);
});

test('a push of old commits counts from when it was pushed', async (t) => {
  clock(t, FRIDAY);
  // Yesterday the author pushed a commit made on 2026-09-10. The timeline
  // shows only the commit's date; the pull_request run shows the push.
  const { github, calls } = fakeRepo({
    pulls: [pull(201, { login: 'uma', requested: ['dmitsh'], labels: ['lifecycle/stale'] })],
    timelines: {
      201: [
        commented(account('uma'), '2026-09-01T10:00:00Z', 'Rebased.'),
        acted('labeled', bot(ACTIONS_BOT), '2026-10-01T06:20:00Z', { label: { name: 'lifecycle/stale' } }),
        committed('2026-09-10T10:00:00Z'),
      ],
    },
    runs: {
      201: [
        workflowRun(account('uma'), '2026-10-22T10:00:05Z'),
        workflowRun(bot('copy-pr-bot[bot]'), '2026-10-22T10:01:00Z', { event: 'push' }),
      ],
    },
  });

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: 201, name: 'lifecycle/stale' } },
  ]);
  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.actions.listWorkflowRunsForRepo').map(({ params }) => params),
    [{ ...REPO, event: 'pull_request', head_sha: 'head201', per_page: 100, page: 1 }],
  );
});

test('pushes by bots do not reset idleness', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [pull(211, { login: 'vic', requested: ['dmitsh'] }), pull(212, { login: 'wes', requested: ['dmitsh'] })],
    timelines: {
      // Dependabot pushed its own commit, dated at the push.
      211: [commented(account('vic'), '2026-10-16T10:00:00Z'), committed('2026-10-22T10:00:00Z', { author: DEPENDABOT_GIT, committer: WEB_FLOW_GIT })],
      // A workflow pushed with GITHUB_TOKEN, as publish-fern-docs.yml does.
      212: [commented(account('wes'), '2026-10-16T10:00:00Z'), committed('2026-10-22T10:00:00Z', { author: ACTIONS_GIT, committer: WEB_FLOW_GIT })],
    },
    // A GITHUB_TOKEN push starts runs that wait for approval, by
    // github-actions[bot] (GitHub's "Events that trigger workflows").
    runs: {
      211: [workflowRun(bot('dependabot[bot]'), '2026-10-22T10:00:05Z')],
      212: [workflowRun(bot(ACTIONS_BOT), '2026-10-22T10:00:05Z')],
    },
  });

  await runNudgeLive(github);

  const posted = comments(calls);
  assert.deepEqual(posted.map((c) => c.issue_number), [211, 212]);
  for (const { body } of posted) assert.ok(body.endsWith(marker('2026-10-16T10:00:00.000Z')), body);
});

test('a pull_request workflow runs on every push, so each push leaves a run', () => {
  // The pull-requests job dates pushes from these runs. Default activity
  // types include synchronize; branch and path filters would skip pushes.
  const dir = join(GITHUB_DIR, 'workflows');
  const onEveryPush = readdirSync(dir)
    .filter((name) => /\.ya?ml$/.test(name))
    .filter((name) => {
      const { on } = loadWorkflow(join(dir, name));
      if (on === 'pull_request' || (Array.isArray(on) && on.includes('pull_request'))) return true;
      if (on === null || typeof on !== 'object' || !('pull_request' in on)) return false;
      const trigger = on.pull_request ?? {};
      const types = trigger.types ?? ['opened', 'synchronize', 'reopened'];
      return types.includes('synchronize') && !['branches', 'branches-ignore', 'paths', 'paths-ignore'].some((key) => key in trigger);
    });
  assert.ok(onEveryPush.length > 0, 'no workflow runs on pull_request synchronize for every pull request');
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

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), []);
});

test('an opted-out pull request still loses lifecycle/stale after activity', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [
      // Active 5 business days ago: the label comes off, and opting out
      // means no reminder.
      pull(241, { login: 'bea', requested: ['dmitsh'], labels: ['lifecycle/stale', 'lifecycle/frozen'] }),
      // Still idle: the label stays, and again no reminder.
      pull(242, { login: 'cal', requested: ['dmitsh'], labels: ['lifecycle/stale', 'do-not-merge'] }),
    ],
    timelines: {
      241: [commented(account('bea'), '2026-10-16T10:00:00Z', 'Picking this up again.')],
      242: [commented(account('cal'), '2026-08-23T10:00:00Z')],
    },
  });

  await runNudgeLive(github);

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.removeLabel', params: { ...REPO, issue_number: 241, name: 'lifecycle/stale' } },
  ]);
});

test('activity past the first timeline page counts', async (t) => {
  clock(t, FRIDAY);
  const events = Array.from({ length: 130 }, (_, i) => commented(account('pat'), githubTime(Date.parse('2026-08-01T00:00:00Z') + i * 60_000)));
  events.push(commented(account('pat'), '2026-10-22T10:00:00Z'));
  const { github, calls } = fakeRepo({ pulls: [pull(171, { login: 'pat', requested: ['dmitsh'] })], timelines: { 171: events } });

  await runNudgeLive(github);

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

  await runNudgeLive(github);

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

test('nudge dry run makes no write call and logs what it would do', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = fakeRepo({
    pulls: [
      // Due a reminder to dmitsh.
      pull(501, { requested: ['dmitsh'] }),
      // Idle 61 days with no reviewer requested: due lifecycle/stale only.
      pull(502),
      // Active yesterday: due to lose lifecycle/stale.
      pull(503, { labels: ['lifecycle/stale'] }),
    ],
    timelines: {
      501: [commented(account('author501'), '2026-10-16T10:00:00Z')],
      502: [commented(account('author502'), '2026-08-23T10:00:00Z')],
      503: [commented(account('author503'), '2026-10-22T10:00:00Z')],
    },
  });

  // The script as shipped.
  const log = await runNudge(github);

  assert.deepEqual(writes(calls), []);
  assert.deepEqual(log.failed, []);
  for (const line of [
    '#501: would remind 1 account(s), idle since 2026-10-16T10:00:00.000Z (dry run)',
    '#502: idle 61 days, would add lifecycle/stale (dry run)',
    '#503: active again, would remove lifecycle/stale (dry run)',
    'Dry run: would remind on 1 of 1 pull requests due a reminder; 0 left for later runs.',
  ]) {
    assert.ok(log.info.includes(line), `missing "${line}" in:\n${log.info.join('\n')}`);
  }
});

test('at most 20 reminders a run, the longest idle first', async (t) => {
  clock(t, FRIDAY);
  // 22 pull requests due a reminder, listed in an order unrelated to how
  // long each has been idle: #601 has been idle the shortest time and #610
  // the next shortest.
  const hoursBefore = [0, 5, 10, 15, 20, 3, 8, 13, 18, 1, 6, 11, 16, 21, 4, 9, 14, 19, 2, 7, 12, 17];
  const pulls = [];
  const timelines = {};
  hoursBefore.forEach((hours, i) => {
    const number = 601 + i;
    pulls.push(pull(number, { requested: ['dmitsh'] }));
    timelines[number] = [commented(account(`author${number}`), githubTime(Date.parse('2026-10-16T10:00:00Z') - hours * 3_600_000))];
  });
  const { github, calls } = fakeRepo({ pulls, timelines });

  const log = await runNudgeLive(github);

  // The 20 longest idle, posted in list order; nothing else is written.
  assert.deepEqual(
    writes(calls).map(({ route, params }) => [route, params.issue_number]),
    [602, 603, 604, 605, 606, 607, 608, 609, 611, 612, 613, 614, 615, 616, 617, 618, 619, 620, 621, 622].map((number) => [
      'rest.issues.createComment',
      number,
    ]),
  );
  const left = log.info.filter((line) => line.includes('left for a later run'));
  assert.deepEqual(left, [
    '#610: reminder due, left for a later run (at most 20 a run)',
    '#601: reminder due, left for a later run (at most 20 a run)',
  ]);
  assert.ok(log.info.includes('Reminded on 20 of 22 pull requests due a reminder; 2 left for later runs.'), log.info.join('\n'));
});

// A fake repository of closed issues and pull requests for the lock job.
// issues.listForRepo sorts the current state on every call, and a comment or
// a lock moves the item's updated_at to now, as on GitHub (actions/toolkit#47
// shows updated_at equal to its locked event, 12 days after it closed).
// createComment is recorded rather than rejected, so a comment attempt shows
// up in calls. `changedAfterList` holds what people change between the list
// and the first issues.get, as { number: fields }.
function closedRepo(items, { changedAfterList = {} } = {}) {
  const state = items.map((item) => ({ ...item }));
  const find = (number) => state.find((item) => item.number === number);
  let pending = changedAfterList;
  return recordingGithub({
    'rest.issues.listForRepo': (params) => {
      const sorted = [...state].sort((a, b) => a.updated_at.localeCompare(b.updated_at) || a.number - b.number);
      return page(sorted.map((item) => ({ ...item })), params);
    },
    'rest.issues.get': ({ issue_number: number }) => {
      for (const [changed, fields] of Object.entries(pending)) Object.assign(find(Number(changed)), fields);
      pending = {};
      return { data: { ...find(number) } };
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

  const log = await runLockLive(github);

  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.issues.listForRepo').map(({ params }) => params),
    [
      { ...REPO, state: 'closed', sort: 'updated', direction: 'asc', per_page: 100, page: 1 },
      { ...REPO, state: 'closed', sort: 'updated', direction: 'asc', per_page: 100, page: 2 },
    ],
  );
  // The 50 oldest, and the count of the rest shows every page up to the
  // first recent item was read before the first lock.
  const expected = [];
  for (let number = 1; number <= 50; number += 1) {
    expected.push({ route: 'rest.issues.lock', params: { ...REPO, issue_number: number, lock_reason: 'resolved' } });
  }
  assert.deepEqual(writes(calls), expected);
  assert.ok(log.info.includes('Locked 50 of 150 eligible threads; 100 left for later runs.'), log.info.join('\n'));
});

test('lock never comments', async (t) => {
  clock(t, FRIDAY);
  // An issue and a pull request, each closed 91 days ago.
  const { github, calls } = closedRepo([closed(7, '2026-07-24T10:00:00Z'), closed(8, '2026-07-24T10:00:00Z')]);

  await runLockLive(github);

  assert.deepEqual(writes(calls), [
    { route: 'rest.issues.lock', params: { ...REPO, issue_number: 7, lock_reason: 'resolved' } },
    { route: 'rest.issues.lock', params: { ...REPO, issue_number: 8, lock_reason: 'resolved' } },
  ]);
  assert.deepEqual(calls.filter(({ route }) => route === 'rest.issues.createComment'), []);
});

test('lock stops at the cap', async (t) => {
  clock(t, FRIDAY);
  const items = [];
  // Already locked, so neither locked again nor counted against the cap.
  for (let number = 1; number <= 5; number += 1) items.push(closed(number, '2026-06-25T10:00:00Z', { locked: true }));
  // 120 eligible, across both list pages.
  for (let number = 6; number <= 125; number += 1) items.push(closed(number, '2026-07-24T10:00:00Z'));
  for (let number = 126; number <= 130; number += 1) items.push(closed(number, '2026-10-13T10:00:00Z'));
  const { github, calls } = closedRepo(items);

  const log = await runLockLive(github);

  const locked = calls.filter(({ route }) => route === 'rest.issues.lock').map(({ params }) => params.issue_number);
  assert.deepEqual(locked, Array.from({ length: 50 }, (_, i) => i + 6));
  // Locks only: no comment and no other write.
  assert.equal(writes(calls).length, 50);
  assert.ok(log.info.includes('Locked 50 of 120 eligible threads; 70 left for later runs.'), log.info.join('\n'));
});

test('lock dry run makes no write call', async (t) => {
  clock(t, FRIDAY);
  const items = [];
  for (let number = 1; number <= 120; number += 1) items.push(closed(number, '2026-07-24T10:00:00Z'));
  for (let number = 121; number <= 125; number += 1) items.push(closed(number, '2026-10-13T10:00:00Z'));
  const { github, calls } = closedRepo(items);

  // The script as shipped.
  const log = await runLock(github);

  assert.deepEqual([...new Set(calls.map(({ route }) => route))], ['rest.issues.listForRepo']);
  assert.deepEqual(
    log.info.filter((line) => /^#\d+: /.test(line)),
    Array.from({ length: 50 }, (_, i) => `#${i + 1}: would lock (dry run)`),
  );
  assert.ok(log.info.includes('Dry run: would lock 50 of 120 eligible threads; 70 left for later runs.'), log.info.join('\n'));
});

test('live lock reads each thread again and skips one changed since the list', async (t) => {
  clock(t, FRIDAY);
  const justNow = '2026-10-23T09:59:00Z';
  const { github, calls } = closedRepo(
    [1, 2, 3, 4, 5].map((number) => closed(number, '2026-07-24T10:00:00Z')),
    {
      // While the job was reading: a comment on #2, #3 reopened, and #4
      // locked by a maintainer. Each moves updated_at.
      changedAfterList: {
        2: { updated_at: justNow },
        3: { state: 'open', closed_at: null, updated_at: justNow },
        4: { locked: true, active_lock_reason: 'off-topic', updated_at: justNow },
      },
    },
  );

  const log = await runLockLive(github);

  assert.deepEqual(writes(calls).map(({ route, params }) => [route, params.issue_number]), [
    ['rest.issues.lock', 1],
    ['rest.issues.lock', 5],
  ]);
  assert.deepEqual(
    calls.filter(({ route }) => route === 'rest.issues.get').map(({ params }) => params.issue_number),
    [1, 2, 3, 4, 5],
  );
  for (const number of [2, 3, 4]) {
    assert.ok(log.info.includes(`#${number}: updated since it was listed, not locked`), log.info.join('\n'));
  }
  assert.ok(log.info.includes('Locked 2 of 5 eligible threads; 0 left for later runs.'), log.info.join('\n'));
});

test('lock skips already locked items', async (t) => {
  clock(t, FRIDAY);
  const { github, calls } = closedRepo([
    closed(1, '2026-06-25T10:00:00Z', { locked: true }),
    closed(2, '2026-07-15T10:00:00Z'),
    closed(3, '2026-10-13T10:00:00Z'),
  ]);

  await runLockLive(github);

  assert.equal(calls.filter(({ route }) => route === 'rest.issues.listForRepo').length, 1);
  assert.deepEqual(writes(calls).map(({ route, params }) => [route, params.issue_number]), [['rest.issues.lock', 2]]);
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
    'pull-requests': { if: GUARD, permissions: { actions: 'read', 'pull-requests': 'write' } },
    lock: { if: GUARD, permissions: { issues: 'write', 'pull-requests': 'write' } },
  });
});
