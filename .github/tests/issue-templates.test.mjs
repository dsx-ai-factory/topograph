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

// The issue template chooser and the labels each issue form applies.
// labels.test.mjs checks that every label a form names is declared; these
// tests check that each form names the right ones. The idle-question job
// closes issues labeled `question`, so only the question form may apply it.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

import { GITHUB_DIR, loadWorkflow } from './lib/harness.mjs';

const ISSUE_TEMPLATE_DIR = join(GITHUB_DIR, 'ISSUE_TEMPLATE');
const CONFIG = join(ISSUE_TEMPLATE_DIR, 'config.yml');
const DOC_FORMS = ['documentation_request_correction.yml', 'documentation_request_new.yml'];
const THIS_REPO = 'dsx-ai-factory/topograph';

// The issue forms: every .yml or .yaml file except the chooser's config.
function forms() {
  return readdirSync(ISSUE_TEMPLATE_DIR)
    .filter((file) => /\.ya?ml$/.test(file) && !/^config\.ya?ml$/.test(file))
    .sort();
}

// A form's `labels`, which GitHub accepts as a list or a comma-separated string.
function formLabels(file) {
  const { labels } = loadWorkflow(join(ISSUE_TEMPLATE_DIR, file)) ?? {};
  const list = typeof labels === 'string' ? labels.split(',') : (labels ?? []);
  return list.map((label) => String(label).trim()).filter(Boolean);
}

test('chooser disables blank issues', () => {
  assert.equal(loadWorkflow(CONFIG).blank_issues_enabled, false);
});

test('chooser links', () => {
  const links = loadWorkflow(CONFIG).contact_links ?? [];
  assert.deepEqual(
    links.map(({ name, url }) => ({ name, url })),
    [
      {
        name: 'Report a security vulnerability',
        url: 'https://www.nvidia.com/object/submit-security-vulnerability.html',
      },
      { name: 'Ask a question', url: 'https://github.com/dsx-ai-factory/topograph/blob/main/docs/community.md' },
      { name: 'Read the documentation', url: 'https://docs.nvidia.com/topograph' },
    ],
  );
  // GitHub requires an `about` on every contact link.
  assert.deepEqual(
    links.filter((link) => typeof link.about !== 'string' || link.about.trim() === '').map((link) => link.name),
    [],
  );
  assert.match(links[0].about, /PSIRT/);
  assert.match(links[0].about, /not a GitHub issue/);
});

test('no legacy markdown templates', () => {
  const markdown = readdirSync(ISSUE_TEMPLATE_DIR, { recursive: true })
    .filter((file) => /\.md$/i.test(file))
    .sort();
  assert.deepEqual(markdown, []);
});

test('every form applies needs-triage', () => {
  const all = forms();
  assert.ok(all.length > 0, 'no issue forms found');
  assert.deepEqual(
    all.filter((file) => !formLabels(file).includes('needs-triage')),
    [],
  );
});

test('only the question form applies question', () => {
  assert.deepEqual(
    forms().filter((file) => formLabels(file).includes('question')),
    ['question.yml'],
  );
});

test('feature requests apply enhancement', () => {
  const labels = formLabels('feature_request.yml');
  assert.ok(labels.includes('enhancement'), `feature_request.yml applies ${JSON.stringify(labels)}`);
});

test('doc forms apply documentation', () => {
  const wrong = DOC_FORMS.map((file) => ({ file, labels: formLabels(file) }))
    .filter(({ labels }) => !labels.includes('documentation'))
    .map(({ file, labels }) => `${file} applies ${JSON.stringify(labels)}`);
  assert.deepEqual(wrong, []);
});

test('templates link only to this repository', () => {
  const foreign = [];
  let own = 0;
  for (const file of readdirSync(ISSUE_TEMPLATE_DIR).sort()) {
    const text = readFileSync(join(ISSUE_TEMPLATE_DIR, file), 'utf8');
    for (const [url, repo] of text.matchAll(/https:\/\/github\.com\/([\w.-]+\/[\w.-]+)/g)) {
      if (repo === THIS_REPO) own += 1;
      else foreign.push(`${file}: ${url}`);
    }
  }
  assert.deepEqual(foreign, []);
  assert.ok(own > 0, `no template links to ${THIS_REPO}, so the URL pattern matched nothing`);
});
