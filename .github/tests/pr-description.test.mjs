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

// Tests for .github/workflows/pr-description.yaml. Each test runs the job's
// github-script step on a pull request body the way the runner does: the body
// arrives in the BODY environment variable, the payload names the author, and
// the fake GitHub client fails the test on any API call. The fixture bodies
// are in fixtures/pr-description/.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  GITHUB_DIR,
  extractStep,
  fakeCore,
  loadWorkflow,
  recordingGithub,
  runGithubScript,
} from './lib/harness.mjs';

const WORKFLOW = join(GITHUB_DIR, 'workflows', 'pr-description.yaml');
const FIXTURES = join(GITHUB_DIR, 'tests', 'fixtures', 'pr-description');
const TEMPLATE = join(GITHUB_DIR, 'PULL_REQUEST_TEMPLATE.md');

const SUMMARY = 'Summary is empty or missing.';
const LINKED_ISSUE =
  'Linked issue: add "Fixes: #N" or "Related: #N", or tick "Trivial fix" when CONTRIBUTING.md exempts the change.';
const TYPE_OF_CHANGE = 'Type of change: tick at least one box.';
const TESTING = 'Testing is empty or missing.';
const ALL_FOUR = [SUMMARY, LINKED_ISSUE, TYPE_OF_CHANGE, TESTING];
const FAILED = 'The pull request description is incomplete; see the job summary.';

const PASS_LOG = {
  info: ['The pull request description has every required part.'],
  warning: [],
  error: [],
  notice: [],
  failed: [],
  summary: [],
};

// A BODY in the caller's environment must not stand in for a missing one.
delete process.env.BODY;

function fixture(name) {
  return readFileSync(join(FIXTURES, name), 'utf8');
}

// Returns `body` with the first `from` replaced by `to`, and fails when
// `from` is absent, so a fixture edit cannot silently change nothing.
function edit(body, from, to) {
  assert.ok(body.includes(from), `the body does not contain ${JSON.stringify(from)}`);
  return body.replace(from, to);
}

// The job summary for a failed check, as fakeCore renders it.
function failureSummary(messages) {
  return (
    '<h3>The pull request description is incomplete</h3>\n' +
    `<ul>${messages.map((m) => `<li>${m}</li>`).join('')}</ul>\n` +
    '<p>Edit the pull request description to fill in each part listed above. Saving the edit re-runs this check.</p>\n'
  );
}

// Runs the check step for a pull request with `body`. `env` is what the
// step's env: block hands the script; by default BODY is the body itself.
async function check(body, { userType = 'User', env = { BODY: body } } = {}) {
  const { script } = extractStep(WORKFLOW, 'pr-description', 'check');
  const { github, calls } = recordingGithub({});
  const { core, log } = fakeCore();
  const context = {
    eventName: 'pull_request_target',
    repo: { owner: 'dsx-ai-factory', repo: 'topograph' },
    payload: { pull_request: { number: 7, body, user: { login: 'contributor', type: userType } } },
  };
  await runGithubScript(script, { github, context, core, env });
  assert.deepEqual(calls, []);
  return log;
}

test('untouched template fails all four rules', async () => {
  const log = await check(readFileSync(TEMPLATE, 'utf8'));
  assert.deepEqual(log.error, ALL_FOUR);
  assert.deepEqual(log.failed, [FAILED]);
  assert.deepEqual(log.summary, [
    '<h3>The pull request description is incomplete</h3>\n' +
      '<ul><li>Summary is empty or missing.</li>' +
      '<li>Linked issue: add "Fixes: #N" or "Related: #N", or tick "Trivial fix" when CONTRIBUTING.md exempts the change.</li>' +
      '<li>Type of change: tick at least one box.</li>' +
      '<li>Testing is empty or missing.</li></ul>\n' +
      '<p>Edit the pull request description to fill in each part listed above. Saving the edit re-runs this check.</p>\n',
  ]);
  assert.deepEqual([log.info, log.notice, log.warning], [[], [], []]);
});

test('complete body passes', async () => {
  assert.deepEqual(await check(fixture('complete.md')), PASS_LOG);
});

test('each rule fails alone', async (t) => {
  const complete = fixture('complete.md');
  const cases = [
    [
      'summary',
      edit(
        complete,
        'Adds a check that the pull request description fills in the template, so a\n' +
          'reviewer can read the change without opening the diff first.\n',
        '',
      ),
      SUMMARY,
    ],
    ['linked issue', edit(complete, 'Fixes: #612 ', 'Fixes: '), LINKED_ISSUE],
    // Area and Checklist still have ticked boxes, so only the Type of change
    // section can satisfy this rule.
    ['type of change', edit(complete, '- [x] Build or CI', '- [ ] Build or CI'), TYPE_OF_CHANGE],
    ['testing', edit(complete, "`node --test '.github/tests/**/*.test.mjs'` passes.\n", ''), TESTING],
  ];
  for (const [name, body, message] of cases) {
    await t.test(name, async () => {
      const log = await check(body);
      assert.deepEqual(log.error, [message]);
      assert.deepEqual(log.failed, [FAILED]);
      assert.deepEqual(log.summary, [failureSummary([message])]);
    });
  }
});

test('trivial fix with no issue passes', async () => {
  const body = fixture('trivial-fix.md');
  assert.match(body, /^Fixes: <!--[^\n]*-->\nRelated: <!--[^\n]*-->\n- \[x\] Trivial fix/m);
  assert.deepEqual(await check(body), PASS_LOG);
});

test('N/A as the issue fails linked issue only', async () => {
  const log = await check(fixture('na-issue.md'));
  assert.deepEqual(log.error, [LINKED_ISSUE]);
  assert.deepEqual(log.failed, [FAILED]);
});

test('linked issue accepts the documented reference forms only', async (t) => {
  const complete = fixture('complete.md');
  const accepted = [
    'Related: #612',
    'fixes: #612',
    'RELATED:#612',
    'Fixes: dsx-ai-factory/topograph#612',
    'Fixes: https://github.com/dsx-ai-factory/topograph/issues/612',
  ];
  const rejected = [
    'Fixes: 612',
    'Fixes: #612abc',
    'Fixes: other-org/topograph#612',
    'Fixes: https://github.com/other-org/topograph/issues/612',
    'Fixes: https://github.com/dsx-ai-factory/topograph/pull/612',
  ];
  for (const line of accepted) {
    await t.test(`accepts ${line}`, async () => {
      assert.deepEqual(await check(edit(complete, 'Fixes: #612', line)), PASS_LOG);
    });
  }
  for (const line of rejected) {
    await t.test(`rejects ${line}`, async () => {
      const log = await check(edit(complete, 'Fixes: #612', line));
      assert.deepEqual(log.error, [LINKED_ISSUE]);
    });
  }
});

test('null body fails all four rules', async () => {
  // GitHub sends `body: null` for an empty description, and the runner turns
  // a null expression into an empty string, so BODY arrives as ''. A step
  // whose env: block lost BODY would leave it unset; both must fail cleanly.
  for (const env of [{ BODY: '' }, {}]) {
    const log = await check(null, { env });
    assert.deepEqual(log.error, ALL_FOUR, `env ${JSON.stringify(env)}`);
    assert.deepEqual(log.failed, [FAILED], `env ${JSON.stringify(env)}`);
  }
});

test('old template fails with all four messages', async () => {
  // A body pasted from the two-section template that preceded this one, with
  // an issue line and ticked boxes outside the sections the rules read.
  const body = fixture('old-template.md');
  assert.match(body, /^Fixes: #612$/m);
  assert.match(body, /^- \[x\] /m);
  const log = await check(body);
  assert.deepEqual(log.error, ALL_FOUR);
  assert.deepEqual(log.failed, [FAILED]);
});

test('heading match is case and whitespace tolerant', async () => {
  let body = fixture('complete.md');
  body = edit(body, '## Summary\n', '## summary\n');
  body = edit(body, '## Linked issue\n', '## Linked issue  \n');
  body = edit(body, '## Type of change\n', '##   TYPE OF CHANGE\t\n');
  body = edit(body, '## Testing\n', '## testing \n');
  assert.deepEqual(await check(body), PASS_LOG);
});

test('crlf body behaves like lf', async () => {
  const crlf = fixture('crlf-complete.md');
  // Guard the fixture itself: if git or an editor rewrote it to LF, this test
  // would compare LF with LF and prove nothing.
  assert.ok(crlf.includes('\r\n'), 'crlf-complete.md has no CRLF line ending');
  assert.doesNotMatch(crlf, /[^\r]\n|^\n/, 'crlf-complete.md has a bare LF line ending');
  assert.equal(crlf.replaceAll('\r\n', '\n'), fixture('complete.md'));
  assert.deepEqual(await check(crlf), PASS_LOG);

  const template = readFileSync(TEMPLATE, 'utf8').replaceAll('\n', '\r\n');
  const log = await check(template);
  assert.deepEqual(log.error, ALL_FOUR);
  assert.deepEqual(log.failed, [FAILED]);
});

test('injection text is data', async () => {
  const marker = `/tmp/gov-pwned-${process.pid}`;
  const body = fixture('injection.md').replaceAll('<pid>', String(process.pid));
  for (const text of [`$(touch ${marker})`, `\`touch ${marker}\``, '${{ secrets.GITHUB_TOKEN }}', '::set-output name=x::y']) {
    assert.ok(body.includes(text), `injection.md does not contain ${text}`);
  }
  assert.equal(existsSync(marker), false, `${marker} exists before the run`);

  const log = await check(body);

  assert.equal(existsSync(marker), false, `${marker} was created`);
  assert.deepEqual(log, PASS_LOG);
  assert.deepEqual(Object.values(log).flat().filter((entry) => entry.includes('::')), []);
});

test('bot author is skipped', async () => {
  const log = await check(readFileSync(TEMPLATE, 'utf8'), { userType: 'Bot' });
  assert.deepEqual(log, {
    info: [],
    warning: [],
    error: [],
    notice: ['Skipped: pull request opened by a bot account.'],
    failed: [],
    summary: [],
  });
});

test('check is reported as "PR description"', () => {
  // main-enforced matches required checks by this name.
  assert.equal(loadWorkflow(WORKFLOW).jobs['pr-description'].name, 'PR description');
});

test('permissions are empty', () => {
  const workflow = loadWorkflow(WORKFLOW);
  assert.deepEqual(workflow.permissions, {});
  assert.deepEqual(workflow.jobs['pr-description'].permissions, {});
});

test('runs upstream on every event that changes the body or the head', () => {
  const workflow = loadWorkflow(WORKFLOW);
  assert.deepEqual(workflow.on, {
    pull_request_target: { types: ['opened', 'edited', 'reopened', 'synchronize'], branches: ['main'] },
  });
  assert.deepEqual(workflow.concurrency, {
    group: '${{ github.workflow }}-${{ github.event.pull_request.number }}',
    'cancel-in-progress': true,
  });
  assert.equal(workflow.jobs['pr-description'].if, "github.repository == 'dsx-ai-factory/topograph'");
  // The body reaches the script only through this env entry; the fixture
  // tests above set BODY directly, so this pins what the runner passes.
  assert.deepEqual(extractStep(WORKFLOW, 'pr-description', 'check').env, {
    BODY: '${{ github.event.pull_request.body }}',
  });
});
