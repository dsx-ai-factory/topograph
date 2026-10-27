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

// Security rules every workflow in .github/workflows/ must keep. Each test
// collects every violation and compares the list with [], so a failure names
// the file, job and step at fault. The rules with fixtures are functions over
// one workflow, so the tests at the end can run them on workflows written to
// break them.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

import { GITHUB_DIR, loadWorkflow } from './lib/harness.mjs';

const WORKFLOWS_DIR = join(GITHUB_DIR, 'workflows');

// Verbatim upstream-only guard, so a fork with Actions enabled never acts on
// its own issues or pull requests.
const UPSTREAM_GUARD = "github.repository == 'dsx-ai-factory/topograph'";

// Triggers that run with the base repository's token on events anyone can
// cause, or on a schedule in every fork that enables Actions.
const GUARDED_TRIGGERS = ['schedule', 'issues', 'issue_comment', 'pull_request_target'];

// Contexts that carry text a contributor controls. Inside `${{ }}` in a run:
// or script: block they are pasted into the code before it runs.
const UNTRUSTED_CONTEXTS = [
  'github.event.pull_request.title',
  'github.event.pull_request.body',
  'github.event.pull_request.head.ref',
  'github.event.pull_request.head.label',
  'github.event.issue.title',
  'github.event.issue.body',
  'github.event.comment.body',
  'github.head_ref',
];

// actions/checkout and actions/cache, including the actions/cache/restore and
// actions/cache/save sub-actions.
const CHECKOUT_OR_CACHE = /^actions\/(checkout|cache)(\/[^@]*)?@/;

// Text on a run: or script: line that checks out or fetches the pull request
// head.
const PR_HEAD_FETCHES = ['gh pr checkout', 'refs/pull/', 'pull/${{'];

// References to the pull request head. On the same line as `git fetch`, or in
// a with.ref expression, they bring the head's code into the job.
const HEAD_REFERENCES = ['head.sha', 'head.ref', 'head_ref'];

const EXPRESSION = /\$\{\{([\s\S]*?)\}\}/g;

const workflows = readdirSync(WORKFLOWS_DIR)
  .filter((file) => /\.ya?ml$/.test(file))
  .sort()
  .map((file) => {
    const path = join(WORKFLOWS_DIR, file);
    return { file, text: readFileSync(path, 'utf8'), wf: loadWorkflow(path) };
  });

function triggers(wf) {
  const on = wf.on;
  if (typeof on === 'string') return [on];
  if (Array.isArray(on)) return on;
  return Object.keys(on ?? {});
}

function jobs(wf) {
  return Object.entries(wf.jobs ?? {});
}

function steps(wf) {
  return jobs(wf).flatMap(([jobId, job]) =>
    (job.steps ?? []).map((step, index) => ({ jobId, step, where: `job ${jobId} step ${step.id ?? step.name ?? index}` })),
  );
}

function holdsIssueOrPrWrite(permissions) {
  if (permissions === 'write-all') return true;
  if (permissions === null || typeof permissions !== 'object') return false;
  return permissions.issues === 'write' || permissions['pull-requests'] === 'write';
}

// Rewrites bracket property access, ['x'] or ["x"] with any spaces inside the
// brackets, as .x and lowercases the result, so the spellings GitHub reads as
// one context compare equal. Expression syntax is case-insensitive.
function normalizeContext(text) {
  return text.replace(/\[\s*(?:'([^']*)'|"([^"]*)")\s*\]/g, (_, single, double) => `.${single ?? double}`).toLowerCase();
}

// The lines of a run: or script: body, with backslash-continued lines joined,
// normalized as contexts are.
function commandLines(body) {
  return normalizeContext(body.replace(/\\\r?\n/g, ' ')).split('\n');
}

// The steps of a pull_request_target workflow that check out or restore
// code, or fetch the pull request head, which would then run with the base
// repository's token.
function prHeadCheckouts({ file, wf }) {
  if (!triggers(wf).includes('pull_request_target')) return [];
  const found = [];
  for (const { step, where } of steps(wf)) {
    if (CHECKOUT_OR_CACHE.test(step.uses ?? '')) found.push(`${file}: ${where} uses ${step.uses}`);
    for (const [field, body] of [['run', step.run], ['with.script', step.with?.script]]) {
      if (typeof body !== 'string') continue;
      for (const line of commandLines(body)) {
        for (const pattern of PR_HEAD_FETCHES) {
          if (line.includes(pattern)) found.push(`${file}: ${where} ${field} contains "${pattern}"`);
        }
        if (!line.includes('git fetch')) continue;
        for (const reference of HEAD_REFERENCES) {
          if (line.includes(reference)) found.push(`${file}: ${where} ${field} contains "git fetch" with "${reference}"`);
        }
      }
    }
    if (typeof step.with?.ref !== 'string') continue;
    for (const [, expression] of step.with.ref.matchAll(EXPRESSION)) {
      const normalized = normalizeContext(expression);
      for (const reference of HEAD_REFERENCES) {
        if (normalized.includes(reference)) found.push(`${file}: ${where} with.ref expands "${reference}"`);
      }
    }
  }
  return found;
}

// The untrusted contexts that `expression`, the text inside one `${{ }}`,
// reads.
function untrustedContextsIn(expression) {
  const normalized = normalizeContext(expression);
  return UNTRUSTED_CONTEXTS.filter((context) => normalized.includes(context));
}

// Every untrusted context that `${{ }}` expands inside a run: or script: block.
function untrustedExpansions({ file, wf }) {
  const found = [];
  for (const { step, where } of steps(wf)) {
    for (const [field, body] of [['run', step.run], ['with.script', step.with?.script]]) {
      if (typeof body !== 'string') continue;
      for (const [, expression] of body.matchAll(EXPRESSION)) {
        for (const context of untrustedContextsIn(expression)) found.push(`${file}: ${where} ${field} expands ${context}`);
      }
    }
  }
  return found;
}

// Splits an expression at its top-level && and || operators, those outside
// parentheses and outside single-quoted string literals, the only literals
// GitHub expressions have. Toggling at every quote also handles the ''
// escape, which closes and reopens the literal. Returns null when the
// parentheses or quotes do not balance.
function splitTopLevel(expression) {
  const operands = [];
  const operators = [];
  let depth = 0;
  let quoted = false;
  let start = 0;
  for (let i = 0; i < expression.length; i += 1) {
    const char = expression[i];
    if (char === "'") {
      quoted = !quoted;
      continue;
    }
    if (quoted) continue;
    if (char === '(') depth += 1;
    else if (char === ')') depth -= 1;
    else if (depth === 0 && (expression.startsWith('&&', i) || expression.startsWith('||', i))) {
      operands.push(expression.slice(start, i).trim());
      operators.push(expression.slice(i, i + 2));
      start = i + 2;
      i += 1;
    }
    if (depth < 0) return null;
  }
  if (quoted || depth !== 0) return null;
  operands.push(expression.slice(start).trim());
  return { operands, operators };
}

// True when a job's if: lets the job run only in the upstream repository:
// trimmed, and without a ${{ }} wrapping the whole of it, the condition is
// the guard, or a top-level && conjunction with the guard as one operand and
// no top-level ||.
function guardGatesWholeCondition(condition) {
  let expression = String(condition ?? '').trim();
  if (expression.startsWith('${{') && expression.endsWith('}}')) expression = expression.slice(3, -2).trim();
  const split = splitTopLevel(expression);
  if (split === null) return false;
  return split.operators.every((operator) => operator === '&&') && split.operands.includes(UPSTREAM_GUARD);
}

// The jobs of a workflow on a guarded trigger that hold issues or
// pull-requests write (`checked`), and those whose if: does not require the
// upstream guard (`found`).
function unguardedJobs({ file, wf }) {
  const checked = [];
  const found = [];
  if (!triggers(wf).some((trigger) => GUARDED_TRIGGERS.includes(trigger))) return { checked, found };
  for (const [jobId, job] of jobs(wf)) {
    // A job without its own permissions block inherits the workflow's.
    if (!holdsIssueOrPrWrite(job.permissions ?? wf.permissions)) continue;
    checked.push(`${file}:${jobId}`);
    if (!guardGatesWholeCondition(job.if)) {
      found.push(`${file}: job ${jobId} holds issues or pull-requests write, but its if: does not require ${UPSTREAM_GUARD}`);
    }
  }
  return { checked, found };
}

const pullRequestTarget = workflows.filter(({ wf }) => triggers(wf).includes('pull_request_target'));

test('the invariants read both .yaml and .yml workflows', () => {
  const files = workflows.map(({ file }) => file);
  assert.ok(files.includes('assign.yaml'), 'assign.yaml is not among the workflows read');
  assert.ok(files.includes('go.yml'), 'go.yml is not among the workflows read');
});

test('pull_request_target workflows never check out code', () => {
  assert.deepEqual(workflows.flatMap(prHeadCheckouts), []);
});

test('pull_request_target workflows reference no secrets', () => {
  const found = [];
  for (const { file, text, wf } of pullRequestTarget) {
    if (text.includes('secrets.')) found.push(`${file}: the file text contains "secrets."`);
    // Comments are gone after parsing, so this catches secrets['X'],
    // toJSON(secrets) and `secrets: inherit` without tripping on prose.
    if (/\bsecrets\b/.test(JSON.stringify(wf))) found.push(`${file}: the workflow names the secrets context`);
  }
  assert.deepEqual(found, []);
});

test('no untrusted expression inside run or script', () => {
  assert.deepEqual(workflows.flatMap(untrustedExpansions), []);
});

test('guarded jobs', () => {
  const results = workflows.map(unguardedJobs);
  const checked = results.flatMap((result) => result.checked);
  assert.deepEqual(results.flatMap((result) => result.found), []);
  // Positive control: the rule must see at least the known write-token job,
  // so a broken trigger or permission filter cannot pass by checking nothing.
  assert.ok(checked.includes('assign.yaml:assign'), `jobs checked: ${checked.join(', ') || 'none'}`);
});

test('every workflow declares top-level permissions', () => {
  const missing = workflows.filter(({ wf }) => !Object.hasOwn(wf, 'permissions')).map(({ file }) => file);
  assert.deepEqual(missing, []);
});

// Rule fixtures. Each workflow below breaks, or deliberately does not break,
// one rule above; messages name it fixture.yaml.

function fixture(on, fixtureSteps, job = {}) {
  return { file: 'fixture.yaml', wf: { on, permissions: {}, jobs: { probe: { ...job, steps: fixtureSteps } } } };
}

// Each untrusted context, then spellings of it that GitHub reads as the same
// property: the dot form and bracket access with either quote, spaces inside
// the brackets and any letter case.
const UNTRUSTED_SPELLINGS = [
  [
    'github.event.pull_request.title',
    'github.event.pull_request.title',
    "github.event.pull_request['title']",
    'github["event"]["pull_request"]["title"]',
  ],
  [
    'github.event.pull_request.body',
    'github.event.pull_request.body',
    "github.event.pull_request[ 'body' ]",
    'GitHub.Event.Pull_Request["Body"]',
  ],
  [
    'github.event.pull_request.head.ref',
    'github.event.pull_request.head.ref',
    "github.event.pull_request['head']['ref']",
  ],
  [
    'github.event.pull_request.head.label',
    'github.event.pull_request.head.label',
    'github.event.pull_request.head["label"]',
  ],
  ['github.event.issue.title', 'github.event.issue.title', "github.event['issue'].title"],
  ['github.event.issue.body', 'github.event.issue.body', "github.event.issue['body']", 'github.event.issue["body"]'],
  ['github.event.comment.body', 'github.event.comment.body', "github['event']['comment']['body']"],
  ['github.head_ref', 'github.head_ref', "github['head_ref']", 'GITHUB.HEAD_REF'],
];

test('the untrusted-text fixtures spell every listed context', () => {
  assert.deepEqual(UNTRUSTED_SPELLINGS.map(([context]) => context).sort(), [...UNTRUSTED_CONTEXTS].sort());
});

test('the untrusted-text rule matches the dot form and bracket access of every context', () => {
  for (const [context, ...spellings] of UNTRUSTED_SPELLINGS) {
    const wf = fixture('push', spellings.map((spelling, index) => ({ id: `s${index}`, run: `echo "\${{ ${spelling} }}"` })));
    assert.deepEqual(
      untrustedExpansions(wf),
      spellings.map((_, index) => `fixture.yaml: job probe step s${index} run expands ${context}`),
      `spellings of ${context}`,
    );
  }
});

test('the untrusted-text rule matches bracket access inside a github-script script', () => {
  const wf = fixture('push', [
    {
      id: 'script',
      uses: 'actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3',
      with: { script: "core.info(`${{ github.event.issue['body'] }}`);" },
    },
  ]);
  assert.deepEqual(untrustedExpansions(wf), ['fixture.yaml: job probe step script with.script expands github.event.issue.body']);
});

test('the untrusted-text rule ignores trusted fields in either form', () => {
  const wf = fixture('push', [
    { id: 'number', run: "echo \"${{ github.event.pull_request['number'] }}\"" },
    { id: 'sha', run: 'echo "${{ github.event.pull_request.head["sha"] }}"' },
  ]);
  assert.deepEqual(untrustedExpansions(wf), []);
});

test('the upstream guard must gate the whole job condition', () => {
  const guard = UPSTREAM_GUARD;
  const cases = [
    // Only the guard, or a top-level && with the guard as one operand.
    [guard, true],
    [`  ${guard}\n`, true],
    [`\${{ ${guard} }}`, true],
    [`github.event_name == 'issues' && ${guard}`, true],
    [`${guard} && (github.event.action == 'opened' || github.event.action == 'edited')`, true],
    // The shape of assign.yaml's condition, with || inside parentheses.
    [
      `${guard} && !github.event.issue.pull_request && (startsWith(github.event.comment.body, '/assign') ||\n startsWith(github.event.comment.body, '/unassign'))`,
      true,
    ],
    // || and ( inside a string literal are not operators.
    [`github.actor != 'a || b' && ${guard}`, true],
    [`github.actor != '(' && ${guard}`, true],
    // A guard that does not gate the whole condition.
    [`${guard} || true`, false],
    [`\${{ ${guard} || true }}`, false],
    [`true || ${guard}`, false],
    [`(${guard} || true) && github.event_name == 'issues'`, false],
    [`${guard} && github.event_name == 'issues' || true`, false],
    // A ${{ }} that wraps only part of the condition is string interpolation,
    // and a non-empty string is truthy.
    [`\${{ ${guard} }} && true`, false],
    // Unbalanced parentheses or quotes.
    [`${guard} && (github.event_name == 'issues'`, false],
    [`${guard} && github.actor == 'a`, false],
    // No guard at all.
    [`github.repository != 'dsx-ai-factory/topograph'`, false],
    ['', false],
    [undefined, false],
  ];
  const wrong = cases
    .filter(([condition, expected]) => guardGatesWholeCondition(condition) !== expected)
    .map(([condition, expected]) => `${JSON.stringify(condition)} should ${expected ? 'pass' : 'fail'}`);
  assert.deepEqual(wrong, []);
});

test('guarded jobs reports a write-token job whose guard is ORed away', () => {
  const wf = fixture({ issues: { types: ['opened'] } }, [{ id: 'noop', run: 'true' }], {
    if: `${UPSTREAM_GUARD} || true`,
    permissions: { issues: 'write' },
  });
  assert.deepEqual(unguardedJobs(wf), {
    checked: ['fixture.yaml:probe'],
    found: [`fixture.yaml: job probe holds issues or pull-requests write, but its if: does not require ${UPSTREAM_GUARD}`],
  });
});

test('the checkout rule finds a pull request head fetched from run, script or with.ref', () => {
  const wf = fixture({ pull_request_target: { types: ['opened'] } }, [
    { id: 'gh', run: 'gh pr checkout "$PR_NUMBER"' },
    { id: 'refs', run: 'git fetch origin refs/pull/7/head' },
    { id: 'number', run: 'git fetch origin pull/${{ github.event.number }}/head:pr' },
    { id: 'sha', run: 'git fetch origin "${{ github.event.pull_request.head.sha }}"' },
    { id: 'continued', run: "git fetch origin \\\n  \"${{ github.event.pull_request['head']['ref'] }}\"" },
    { id: 'env', run: 'git fetch origin "$GITHUB_HEAD_REF"' },
    {
      id: 'script',
      uses: 'actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3',
      with: { script: 'execSync(`git fetch origin ${context.payload.pull_request.head.sha}`);' },
    },
    {
      id: 'ref',
      uses: 'example/fetch-code@0000000000000000000000000000000000000000',
      with: { ref: '${{ github.event.pull_request.head.sha }}' },
    },
  ]);
  assert.deepEqual(prHeadCheckouts(wf), [
    'fixture.yaml: job probe step gh run contains "gh pr checkout"',
    'fixture.yaml: job probe step refs run contains "refs/pull/"',
    'fixture.yaml: job probe step number run contains "pull/${{"',
    'fixture.yaml: job probe step sha run contains "git fetch" with "head.sha"',
    'fixture.yaml: job probe step continued run contains "git fetch" with "head.ref"',
    'fixture.yaml: job probe step env run contains "git fetch" with "head_ref"',
    'fixture.yaml: job probe step script with.script contains "git fetch" with "head.sha"',
    'fixture.yaml: job probe step ref with.ref expands "head.sha"',
  ]);
});

test('the checkout rule pairs git fetch with a head reference only on the same command line', () => {
  // The shape of a script that reads the head SHA from the API and posts
  // fix-up instructions that mention git fetch: it fetches nothing.
  const wf = fixture({ pull_request_target: { types: ['opened'] } }, [
    {
      id: 'script',
      uses: 'actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3',
      with: { script: 'const head = pull.head.sha;\nconst help = `git fetch upstream`;\n' },
    },
    { id: 'base', uses: 'example/fetch-code@0000000000000000000000000000000000000000', with: { ref: '${{ github.sha }}' } },
  ]);
  assert.deepEqual(prHeadCheckouts(wf), []);
});

test('the checkout rule applies only to pull_request_target workflows', () => {
  const wf = fixture({ pull_request: { types: ['opened'] } }, [
    { id: 'gh', run: 'gh pr checkout "$PR_NUMBER"' },
    { id: 'checkout', uses: 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' },
  ]);
  assert.deepEqual(prHeadCheckouts(wf), []);
});
