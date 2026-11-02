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

// Test harness for the scripts inside .github/workflows/. A test pulls a
// step out of a workflow file with extractStep and runs its script here,
// against a recorded fake of the GitHub API, so no test calls GitHub or
// needs a token. YAML is parsed by the mikefarah yq v4 binary, which the
// ubuntu-latest runner image ships, so the tests need no npm packages.

import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// The repository's .github/ directory, wherever the tests are run from.
export const GITHUB_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

const AsyncFunction = Object.getPrototypeOf(async () => null).constructor;

// Parses a YAML file (a workflow, an issue form, labels.yml) with yq.
export function loadWorkflow(workflowPath) {
  const result = spawnSync('yq', ['-o=json', '.', workflowPath], { encoding: 'utf8' });
  if (result.error) throw new Error(`cannot run yq on ${workflowPath}: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`yq failed on ${workflowPath}: ${result.stderr.trim()}`);
  return JSON.parse(result.stdout);
}

// Returns the parts of step `stepId` (its `id:`) in job `jobId` that a test
// runs or inspects. `script` is the step's `with.script`.
export function extractStep(workflowPath, jobId, stepId) {
  const workflow = loadWorkflow(workflowPath);
  const step = (workflow?.jobs?.[jobId]?.steps ?? []).find((s) => s?.id === stepId);
  if (!step) throw new Error(`step ${stepId} not found in job ${jobId} of ${workflowPath}`);
  const extracted = {};
  if (step.run !== undefined) extracted.run = step.run;
  if (step.with?.script !== undefined) extracted.script = step.with.script;
  if (step.env !== undefined) extracted.env = step.env;
  if (step.uses !== undefined) extracted.uses = step.uses;
  if (step.with !== undefined) extracted.with = step.with;
  return extracted;
}

// Runs an actions/github-script `script` the way v9.0.0's callAsyncFunction
// does: the source becomes the body of an AsyncFunction whose parameters are
// the injected names. Only require, github, context and core are injected;
// a script that uses another v9 binding (octokit, getOctokit, exec, glob, io)
// fails here with a ReferenceError. `env` is applied to process.env for the
// call, as the step's `env:` block would be, and restored afterwards.
export async function runGithubScript(script, { github, context, core, env = {} }) {
  const saved = new Map();
  for (const [key, value] of Object.entries(env)) {
    saved.set(key, Object.hasOwn(process.env, key) ? process.env[key] : undefined);
    process.env[key] = value;
  }
  try {
    const fn = new AsyncFunction('require', 'github', 'context', 'core', script);
    return await fn(createRequire(import.meta.url), github, context, core);
  } finally {
    for (const [key, value] of saved) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
  }
}

// Runs a `run:` block with the runner's default Linux shell,
// `bash --noprofile --norc -eo pipefail {0}`. The environment is PATH plus
// `env` plus GITHUB_STEP_SUMMARY, so the result does not depend on the
// caller's environment. `summary` is what the script wrote to the summary.
export function runBash(script, env) {
  const dir = mkdtempSync(join(tmpdir(), 'workflow-test-'));
  try {
    const file = join(dir, 'step.sh');
    const summaryFile = join(dir, 'step-summary.md');
    writeFileSync(file, script);
    writeFileSync(summaryFile, '');
    const result = spawnSync('bash', ['--noprofile', '--norc', '-eo', 'pipefail', file], {
      encoding: 'utf8',
      env: { PATH: process.env.PATH, ...env, GITHUB_STEP_SUMMARY: summaryFile },
    });
    if (result.error) throw new Error(`cannot run bash: ${result.error.message}`);
    return {
      status: result.status,
      stdout: result.stdout,
      stderr: result.stderr,
      summary: readFileSync(summaryFile, 'utf8'),
    };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

// A stand-in for @actions/core that records what a script reports. The
// summary renders HTML the way @actions/core does and, like it, reaches the
// log only when write() is called; each write() adds one entry.
export function fakeCore() {
  const log = { info: [], warning: [], error: [], notice: [], failed: [], summary: [] };
  let buffer = '';
  const summary = {
    addHeading(text, level = 1) {
      const tag = ['1', '2', '3', '4', '5', '6'].includes(String(level)) ? `h${level}` : 'h1';
      buffer += `<${tag}>${text}</${tag}>\n`;
      return summary;
    },
    addRaw(text, addEOL = false) {
      buffer += addEOL ? `${text}\n` : text;
      return summary;
    },
    addList(items, ordered = false) {
      const tag = ordered ? 'ol' : 'ul';
      buffer += `<${tag}>${items.map((item) => `<li>${item}</li>`).join('')}</${tag}>\n`;
      return summary;
    },
    async write() {
      log.summary.push(buffer);
      buffer = '';
      return summary;
    },
  };
  const core = {
    info: (message) => log.info.push(String(message)),
    warning: (message) => log.warning.push(String(message)),
    error: (message) => log.error.push(String(message)),
    notice: (message) => log.notice.push(String(message)),
    // Unlike the real setFailed, this does not set process.exitCode, which
    // would fail the test runner itself.
    setFailed: (message) => log.failed.push(String(message)),
    summary,
  };
  return { core, log };
}

// A fake Octokit. `routes` maps a method path such as 'rest.issues.addLabels'
// to a handler that receives the call's parameters and returns what Octokit
// would resolve to, normally `{ data }`. Every call to a provided route is
// recorded in `calls`; calling any other route throws. A handler that throws
// becomes a rejected promise, as a failed API call does.
export function recordingGithub(routes) {
  const calls = [];

  function route(path) {
    const name = path.join('.');
    return new Proxy(function octokitRoute() {}, {
      get(_target, property) {
        // Not thenable, so `await github.rest` does not hang.
        if (typeof property === 'symbol' || property === 'then') return undefined;
        return route([...path, property]);
      },
      apply(_target, _this, args) {
        const handler = routes[name];
        if (typeof handler !== 'function') throw new Error(`unexpected call ${name}`);
        const params = args[0] ?? {};
        calls.push({ route: name, params });
        return Promise.resolve().then(() => handler(...args));
      },
    });
  }

  // Calls `fn` with page 1, 2, ... until a page holds fewer than per_page
  // items (GitHub's default page size is 30), and concatenates the pages.
  async function paginate(fn, params = {}) {
    const perPage = params.per_page ?? 30;
    const all = [];
    for (let page = 1; ; page += 1) {
      const response = await fn({ ...params, page });
      if (!Array.isArray(response?.data)) throw new Error('paginate: the route did not return { data: [] }');
      all.push(...response.data);
      if (response.data.length < perPage) return all;
    }
  }

  const root = route([]);
  const github = new Proxy(root, {
    get(target, property) {
      if (property === 'paginate') return paginate;
      return Reflect.get(target, property);
    },
  });
  return { github, calls };
}

// The label names declared in .github/labels.yml.
export function loadLabels() {
  const entries = loadWorkflow(join(GITHUB_DIR, 'labels.yml')) ?? [];
  return new Set(entries.map((entry) => entry.name));
}

// One string literal: '...', "..." or a `...` with no ${} substitution.
const LITERAL = /'([^'\\\n]*)'|"([^"\\\n]*)"|`([^`\\$]*)`/g;
const LABEL_CALL = /\b(addLabels|setLabels|removeLabel)\s*\(\s*\{([\s\S]*?)\}\s*\)/g;

function literals(text) {
  return [...text.matchAll(LITERAL)].map((m) => m[1] ?? m[2] ?? m[3]);
}

// The label names a github-script source passes as string literals to
// addLabels or setLabels (`labels: [...]`) or to removeLabel (`name: ...`),
// in source order. Labels held in variables or built with ${} are not seen.
export function labelsInScript(script) {
  const found = [];
  for (const [, method, args] of script.matchAll(LABEL_CALL)) {
    if (method === 'removeLabel') {
      const name = args.match(/\bname\s*:\s*('[^'\\\n]*'|"[^"\\\n]*"|`[^`\\$]*`)/);
      if (name) found.push(...literals(name[1]));
    } else {
      const list = args.match(/\blabels\s*:\s*\[([^\]]*)\]/);
      if (list) found.push(...literals(list[1]));
    }
  }
  return found;
}

// The label names in an actions/stale step's `with:` inputs. Every v11.0.0
// input whose name contains "label" holds a comma-separated label list.
export function labelsInStaleInputs(withInputs) {
  return Object.entries(withInputs)
    .filter(([key]) => key.includes('label'))
    .flatMap(([, value]) => String(value).split(','))
    .map((label) => label.trim())
    .filter(Boolean);
}
