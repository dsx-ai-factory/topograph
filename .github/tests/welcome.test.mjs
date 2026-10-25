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

// Tests for .github/workflows/welcome.yaml. Each job's welcome step runs
// against a recorded fake of the GitHub API, and each test compares the
// calls the step made with what it should have made. The last tests read the
// workflow itself: its triggers and the token each job holds.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';

import { GITHUB_DIR, extractStep, fakeCore, loadWorkflow, recordingGithub, runGithubScript } from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'welcome.yaml');
const REPO = { owner: 'dsx-ai-factory', repo: 'topograph' };
const ISSUE_NUMBER = 42;
const PR_NUMBER = 43;
const AUTHOR = 'new-contributor';

const BLOB = 'https://github.com/dsx-ai-factory/topograph/blob/main';
const ISSUE_FIRST_LINK = `${BLOB}/CONTRIBUTING.md#open-an-issue-first`;
const TURNAROUND_LINK = `${BLOB}/CONTRIBUTING.md#turnaround`;
const PR_TEMPLATE_LINK = `${BLOB}/.github/PULL_REQUEST_TEMPLATE.md`;

// An item as GET /repos/{owner}/{repo}/issues lists it. Pull requests are
// listed too and carry a `pull_request` key; issues have none. Issues and
// pull requests share one number sequence in a repository.
function listedIssue(number, state = 'open') {
  return { number, state, user: { login: AUTHOR } };
}

// A closed pull request that never merged keeps `merged_at: null`.
function listedPullRequest(number, state = 'open') {
  return {
    number,
    state,
    user: { login: AUTHOR },
    pull_request: { url: `https://api.github.com/repos/dsx-ai-factory/topograph/pulls/${number}`, merged_at: null },
  };
}

// Runs the issue job's welcome step for issue `number`, opened by AUTHOR.
// `pages` are the pages listForRepo serves for the author, page 1 first.
async function runIssueWelcome({ number = ISSUE_NUMBER, association = 'NONE', type = 'User', pages = [[listedIssue(number)]] } = {}) {
  const { script } = extractStep(WORKFLOW, 'issue', 'welcome');
  const { github, calls } = recordingGithub({
    'rest.issues.listForRepo': ({ page }) => ({ data: pages[page - 1] ?? [] }),
    'rest.issues.createComment': ({ issue_number, body }) => ({ data: { id: 1, issue_number, body } }),
  });
  const { core } = fakeCore();
  const context = {
    eventName: 'issues',
    repo: REPO,
    payload: {
      action: 'opened',
      issue: { number, user: { login: AUTHOR, type }, author_association: association },
    },
  };
  await runGithubScript(script, { github, context, core });
  return calls;
}

// Runs the pull-request job's welcome step for pull request PR_NUMBER.
// `pages` are the pages listForRepo serves for the author, page 1 first.
async function runPullRequestWelcome({ association, type = 'User', pages = [[listedPullRequest(PR_NUMBER)]] }) {
  const { script } = extractStep(WORKFLOW, 'pull-request', 'welcome');
  const { github, calls } = recordingGithub({
    'rest.issues.listForRepo': ({ page }) => ({ data: pages[page - 1] ?? [] }),
    'rest.issues.createComment': ({ issue_number, body }) => ({ data: { id: 1, issue_number, body } }),
  });
  const { core } = fakeCore();
  const context = {
    eventName: 'pull_request_target',
    repo: REPO,
    payload: {
      action: 'opened',
      pull_request: { number: PR_NUMBER, user: { login: AUTHOR, type }, author_association: association },
    },
  };
  await runGithubScript(script, { github, context, core });
  return calls;
}

// The one comment the step posted. Fails unless exactly one was posted, on
// `number`, in this repository.
function onlyComment(calls, number) {
  const comments = calls.filter(({ route }) => route === 'rest.issues.createComment');
  assert.equal(comments.length, 1, `expected one comment, got ${comments.length}`);
  const { owner, repo, issue_number, body } = comments[0].params;
  assert.deepEqual({ owner, repo, issue_number }, { ...REPO, issue_number: number });
  assert.equal(typeof body, 'string');
  return body;
}

// The listForRepo query for AUTHOR's items, as the step must send it.
const LIST_QUERY = { owner: 'dsx-ai-factory', repo: 'topograph', creator: AUTHOR, state: 'all', per_page: 100 };

test('first issue is welcomed once', async () => {
  const calls = await runIssueWelcome();
  assert.deepEqual(
    calls.map(({ route }) => route),
    ['rest.issues.listForRepo', 'rest.issues.createComment'],
  );
  assert.deepEqual(calls[0].params, { ...LIST_QUERY, page: 1 });
  const body = onlyComment(calls, ISSUE_NUMBER);
  assert.ok(body.includes(ISSUE_FIRST_LINK), `no link to ${ISSUE_FIRST_LINK}:\n${body}`);
  assert.ok(body.includes('`/assign`'), `no /assign:\n${body}`);
  assert.ok(body.includes('5 business days'), `no turnaround target:\n${body}`);
  assert.ok(body.includes(TURNAROUND_LINK), `no link to ${TURNAROUND_LINK}:\n${body}`);
});

test('second issue is not welcomed', async () => {
  const calls = await runIssueWelcome({ pages: [[listedIssue(ISSUE_NUMBER), listedIssue(7)]] });
  assert.deepEqual(calls, [{ route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } }]);
});

test('second issue past the first page is not welcomed', async () => {
  // A full first page of pull requests, then an older issue on page 2.
  const firstPage = [listedIssue(ISSUE_NUMBER), ...Array.from({ length: 99 }, (_, i) => listedPullRequest(100 + i))];
  const calls = await runIssueWelcome({ pages: [firstPage, [listedIssue(7)]] });
  assert.deepEqual(calls, [
    { route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } },
    { route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 2 } },
  ]);
});

test('issue author whose only other items are PRs is welcomed', async () => {
  const calls = await runIssueWelcome({
    association: 'CONTRIBUTOR',
    pages: [[listedPullRequest(9), listedIssue(ISSUE_NUMBER), listedPullRequest(3)]],
  });
  assert.deepEqual(
    calls.map(({ route }) => route),
    ['rest.issues.listForRepo', 'rest.issues.createComment'],
  );
  assert.ok(onlyComment(calls, ISSUE_NUMBER).includes(ISSUE_FIRST_LINK));
});

test('org member issue is not welcomed', async () => {
  // The listing would say first issue; only the association stops the welcome.
  const calls = await runIssueWelcome({ association: 'MEMBER' });
  assert.deepEqual(calls, []);
});

test('OWNER issue author is not welcomed', async () => {
  // The listing would say first issue; only the association stops the welcome.
  const calls = await runIssueWelcome({ association: 'OWNER' });
  assert.deepEqual(calls, []);
});

test('COLLABORATOR issue author is not welcomed', async () => {
  // The listing would say first issue; only the association stops the welcome.
  const calls = await runIssueWelcome({ association: 'COLLABORATOR' });
  assert.deepEqual(calls, []);
});

// A new author opens two issues seconds apart. Both runs list both issues,
// so a count of the author's issues is 2 in each run.
const TWO_QUICK_ISSUES = [[listedIssue(ISSUE_NUMBER + 1), listedIssue(ISSUE_NUMBER)]];

test('two quick issues: the lower number is welcomed', async () => {
  const calls = await runIssueWelcome({ number: ISSUE_NUMBER, pages: TWO_QUICK_ISSUES });
  assert.deepEqual(
    calls.map(({ route }) => route),
    ['rest.issues.listForRepo', 'rest.issues.createComment'],
  );
  assert.ok(onlyComment(calls, ISSUE_NUMBER).includes(ISSUE_FIRST_LINK));
});

test('two quick issues: the higher number is not welcomed', async () => {
  const calls = await runIssueWelcome({ number: ISSUE_NUMBER + 1, pages: TWO_QUICK_ISSUES });
  assert.deepEqual(calls, [{ route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } }]);
});

test('bot issue is not welcomed', async () => {
  // The listing would say first issue; only the account type stops the welcome.
  const calls = await runIssueWelcome({ type: 'Bot' });
  assert.deepEqual(calls, []);
});

test('first-time PR is welcomed', async () => {
  const calls = await runPullRequestWelcome({ association: 'FIRST_TIME_CONTRIBUTOR' });
  assert.deepEqual(calls.map(({ route }) => route), ['rest.issues.listForRepo', 'rest.issues.createComment']);
  const body = onlyComment(calls, PR_NUMBER);
  assert.ok(body.includes('5 business days'), `no turnaround target:\n${body}`);
  assert.ok(body.includes(TURNAROUND_LINK), `no link to ${TURNAROUND_LINK}:\n${body}`);
});

test('brand-new GitHub user PR is welcomed', async () => {
  // FIRST_TIMER: the author has never committed anywhere on GitHub.
  const calls = await runPullRequestWelcome({ association: 'FIRST_TIMER' });
  assert.deepEqual(calls.map(({ route }) => route), ['rest.issues.listForRepo', 'rest.issues.createComment']);
  onlyComment(calls, PR_NUMBER);
});

test('first PR is welcomed when the listing holds only that PR', async () => {
  const calls = await runPullRequestWelcome({
    association: 'FIRST_TIME_CONTRIBUTOR',
    pages: [[listedPullRequest(PR_NUMBER)]],
  });
  assert.deepEqual(calls[0], { route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } });
  assert.deepEqual(calls.map(({ route }) => route), ['rest.issues.listForRepo', 'rest.issues.createComment']);
  onlyComment(calls, PR_NUMBER);
});

// FIRST_TIME_CONTRIBUTOR stays on every pull request an author opens until
// one of their commits merges, so the association alone cannot tell a first
// pull request from a second one.
test("first-timer's second PR while the first is open is not welcomed", async () => {
  const calls = await runPullRequestWelcome({
    association: 'FIRST_TIME_CONTRIBUTOR',
    pages: [[listedPullRequest(PR_NUMBER), listedPullRequest(30, 'open')]],
  });
  assert.deepEqual(calls, [{ route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } }]);
});

test("first-timer's PR after a first PR closed unmerged is not welcomed", async () => {
  const calls = await runPullRequestWelcome({
    association: 'FIRST_TIME_CONTRIBUTOR',
    pages: [[listedPullRequest(PR_NUMBER), listedPullRequest(30, 'closed')]],
  });
  assert.deepEqual(calls, [{ route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } }]);
});

test('earlier issues by the same author do not block the PR welcome', async () => {
  const calls = await runPullRequestWelcome({
    association: 'FIRST_TIME_CONTRIBUTOR',
    pages: [[listedPullRequest(PR_NUMBER), listedIssue(12, 'closed'), listedIssue(7)]],
  });
  assert.deepEqual(calls[0], { route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } });
  assert.deepEqual(calls.map(({ route }) => route), ['rest.issues.listForRepo', 'rest.issues.createComment']);
  onlyComment(calls, PR_NUMBER);
});

test('lower-numbered PR on the second page blocks the welcome', async () => {
  // A full first page (this pull request and 99 later issues), then an
  // earlier pull request on page 2: 101 items in all.
  const firstPage = [listedPullRequest(PR_NUMBER), ...Array.from({ length: 99 }, (_, i) => listedIssue(100 + i))];
  const calls = await runPullRequestWelcome({
    association: 'FIRST_TIME_CONTRIBUTOR',
    pages: [firstPage, [listedPullRequest(7, 'closed')]],
  });
  assert.deepEqual(calls, [
    { route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 1 } },
    { route: 'rest.issues.listForRepo', params: { ...LIST_QUERY, page: 2 } },
  ]);
});

test('returning contributor PR is not welcomed', async () => {
  const calls = await runPullRequestWelcome({ association: 'CONTRIBUTOR' });
  assert.deepEqual(calls, []);
});

test('bot PR is not welcomed', async () => {
  // A first-time association, so only the account type stops the welcome.
  const calls = await runPullRequestWelcome({ association: 'FIRST_TIME_CONTRIBUTOR', type: 'Bot' });
  assert.deepEqual(calls, []);
});

test('PR welcome links the PR template and signing', async () => {
  const body = onlyComment(await runPullRequestWelcome({ association: 'FIRST_TIME_CONTRIBUTOR' }), PR_NUMBER);
  assert.ok(body.includes(PR_TEMPLATE_LINK), `no link to ${PR_TEMPLATE_LINK}:\n${body}`);
  assert.ok(body.includes('`git commit -s -S`'), `no git commit -s -S:\n${body}`);
  assert.ok(body.includes('DCO'), `no DCO:\n${body}`);
});

test('welcome text has no emoji or em-dash', async () => {
  const bodies = [
    onlyComment(await runIssueWelcome(), ISSUE_NUMBER),
    onlyComment(await runPullRequestWelcome({ association: 'FIRST_TIME_CONTRIBUTOR' }), PR_NUMBER),
  ];
  // The ranges the repository's text style bans, plus U+2014.
  const banned = /[\u{1F000}-\u{1F0FF}\u{1F1E6}-\u{1F1FF}\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}\u{2B00}-\u{2BFF}\u{FE0F}\u{2014}]/u;
  assert.deepEqual(bodies.filter((body) => banned.test(body)), []);
});

// "Comments once" rests on the triggers: neither script looks for an earlier
// welcome, so a reopened, edited or synchronize run would post another one.
test('triggers are opened only', () => {
  const wf = loadWorkflow(WORKFLOW);
  assert.deepEqual(Object.keys(wf.on), ['issues', 'pull_request_target']);
  assert.deepEqual(wf.on.issues.types, ['opened']);
  assert.deepEqual(wf.on.pull_request_target.types, ['opened']);
});

// Both jobs run for anyone: on any opened issue, and on every fork pull
// request through pull_request_target. A job's permissions block is the only
// thing that limits its token, so pin it exactly.
test('job permissions are exact', () => {
  const wf = loadWorkflow(WORKFLOW);
  assert.deepEqual(wf.permissions, {});
  assert.deepEqual(wf.jobs.issue.permissions, { issues: 'write' });
  assert.deepEqual(wf.jobs['pull-request'].permissions, { issues: 'read', 'pull-requests': 'write' });
});
