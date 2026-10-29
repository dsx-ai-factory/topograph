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

// Every label the repository applies must be declared in .github/labels.yml.
// GitHub does not create a missing label for a token that holds only
// pull-requests: write, so an undeclared label fails the job that applies it,
// and an issue form that names one leaves the issue unlabeled.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

import {
  GITHUB_DIR,
  labelsInScript,
  labelsInStaleInputs,
  loadLabels,
  loadWorkflow,
} from './lib/harness.mjs';

const ISSUE_TEMPLATE_DIR = join(GITHUB_DIR, 'ISSUE_TEMPLATE');
const WORKFLOWS_DIR = join(GITHUB_DIR, 'workflows');
const LABELER = join(GITHUB_DIR, 'labeler.yml');

function yamlFiles(dir) {
  return readdirSync(dir).filter((file) => /\.ya?ml$/.test(file)).sort();
}

function undeclared(names) {
  const declared = loadLabels();
  return [...new Set(names)].filter((name) => !declared.has(name)).sort();
}

test('labels.yml entries have a unique name, a 6-hex color and a description', () => {
  const entries = loadWorkflow(join(GITHUB_DIR, 'labels.yml'));
  assert.ok(Array.isArray(entries) && entries.length > 0, 'labels.yml is not a non-empty list');
  const problems = [];
  const seen = new Set();
  entries.forEach((entry, index) => {
    const name = entry?.name;
    if (typeof name !== 'string' || name === '') problems.push(`entry ${index}: no name`);
    else if (seen.has(name)) problems.push(`entry ${index}: duplicate name ${name}`);
    else seen.add(name);
    if (typeof entry?.color !== 'string' || !/^[0-9a-fA-F]{6}$/.test(entry.color)) {
      problems.push(`entry ${index}: color ${JSON.stringify(entry?.color)} is not a quoted 6-hex string without #`);
    }
    if (typeof entry?.description !== 'string' || entry.description === '') {
      problems.push(`entry ${index}: no description`);
    }
  });
  assert.deepEqual(problems, []);
});

test('every issue-form label is declared', () => {
  const applied = [];
  for (const file of yamlFiles(ISSUE_TEMPLATE_DIR).filter((f) => !/^config\.ya?ml$/.test(f))) {
    const form = loadWorkflow(join(ISSUE_TEMPLATE_DIR, file));
    const labels = typeof form.labels === 'string' ? form.labels.split(',') : (form.labels ?? []);
    applied.push(...labels.map((label) => String(label).trim()).filter(Boolean));
  }
  assert.ok(applied.length > 0, 'no issue form applies a label');
  assert.deepEqual(undeclared(applied), []);
});

test(
  'every labeler.yml label is declared',
  { skip: existsSync(LABELER) ? false : '.github/labeler.yml is absent' },
  () => {
    const config = loadWorkflow(LABELER);
    assert.deepEqual(undeclared(Object.keys(config ?? {})), []);
  },
);

test('every label named in a workflow script is declared', () => {
  const named = [];
  for (const file of yamlFiles(WORKFLOWS_DIR)) {
    const wf = loadWorkflow(join(WORKFLOWS_DIR, file));
    for (const job of Object.values(wf.jobs ?? {})) {
      for (const step of job.steps ?? []) {
        if (typeof step.with?.script === 'string') named.push(...labelsInScript(step.with.script));
        if (/^actions\/stale@/.test(step.uses ?? '')) named.push(...labelsInStaleInputs(step.with ?? {}));
      }
    }
  }
  assert.deepEqual(undeclared(named), []);
});

test('labelsInScript finds the literals passed to addLabels, setLabels and removeLabel', () => {
  const script = [
    "await github.rest.issues.addLabels({ ...common, labels: ['needs-rebase', \"size/XS\"] });",
    'await github.rest.issues.setLabels({',
    '  ...common,',
    '  labels: [`area/ci`],',
    '});',
    "await github.rest.issues.removeLabel({ ...common, name: 'lifecycle/stale' });",
    // Dynamic values are not literals, so they are not reported.
    'await github.rest.issues.addLabels({ ...common, labels: [`size/${size}`, LABEL] });',
    'await github.rest.issues.removeLabel({ ...common, name });',
    // Strings outside a label call are not labels.
    "core.info('not-a-label');",
  ].join('\n');
  assert.deepEqual(labelsInScript(script), ['needs-rebase', 'size/XS', 'area/ci', 'lifecycle/stale']);
});

test('labelsInStaleInputs splits every label input of actions/stale', () => {
  const inputs = {
    'stale-issue-label': 'lifecycle/stale',
    'exempt-issue-labels': 'lifecycle/frozen, help wanted',
    'only-issue-labels': 'question',
    'labels-to-add-when-unstale': '',
    'days-before-issue-stale': 90,
    'stale-issue-message': 'This question has had no activity.',
  };
  assert.deepEqual(labelsInStaleInputs(inputs), ['lifecycle/stale', 'lifecycle/frozen', 'help wanted', 'question']);
});
