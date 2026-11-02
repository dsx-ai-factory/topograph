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
// the file, job and step at fault.

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

const pullRequestTarget = workflows.filter(({ wf }) => triggers(wf).includes('pull_request_target'));

test('the invariants read both .yaml and .yml workflows', () => {
  const files = workflows.map(({ file }) => file);
  assert.ok(files.includes('assign.yaml'), 'assign.yaml is not among the workflows read');
  assert.ok(files.includes('go.yml'), 'go.yml is not among the workflows read');
});

test('pull_request_target workflows never check out code', () => {
  const found = [];
  for (const { file, wf } of pullRequestTarget) {
    for (const { step, where } of steps(wf)) {
      if (CHECKOUT_OR_CACHE.test(step.uses ?? '')) found.push(`${file}: ${where} uses ${step.uses}`);
    }
  }
  assert.deepEqual(found, []);
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
  const found = [];
  for (const { file, wf } of workflows) {
    for (const { step, where } of steps(wf)) {
      for (const [field, body] of [['run', step.run], ['with.script', step.with?.script]]) {
        if (typeof body !== 'string') continue;
        for (const [, expression] of body.matchAll(EXPRESSION)) {
          const lowered = expression.toLowerCase();
          for (const context of UNTRUSTED_CONTEXTS) {
            if (lowered.includes(context)) found.push(`${file}: ${where} ${field} expands ${context}`);
          }
        }
      }
    }
  }
  assert.deepEqual(found, []);
});

test('guarded jobs', () => {
  const checked = [];
  const found = [];
  for (const { file, wf } of workflows) {
    if (!triggers(wf).some((trigger) => GUARDED_TRIGGERS.includes(trigger))) continue;
    for (const [jobId, job] of jobs(wf)) {
      // A job without its own permissions block inherits the workflow's.
      if (!holdsIssueOrPrWrite(job.permissions ?? wf.permissions)) continue;
      checked.push(`${file}:${jobId}`);
      if (!String(job.if ?? '').includes(UPSTREAM_GUARD)) {
        found.push(`${file}: job ${jobId} holds issues or pull-requests write without ${UPSTREAM_GUARD}`);
      }
    }
  }
  assert.deepEqual(found, []);
  // Positive control: the rule must see at least the known write-token job,
  // so a broken trigger or permission filter cannot pass by checking nothing.
  assert.ok(checked.includes('assign.yaml:assign'), `jobs checked: ${checked.join(', ') || 'none'}`);
});

test('every workflow declares top-level permissions', () => {
  const missing = workflows.filter(({ wf }) => !Object.hasOwn(wf, 'permissions')).map(({ file }) => file);
  assert.deepEqual(missing, []);
});
