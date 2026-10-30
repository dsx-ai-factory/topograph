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

// Dependabot must wait at least 7 days after a release before it proposes the
// update, on every ecosystem, for every update type and every dependency. 7
// days is the threshold zizmor's dependabot-cooldown audit applies by default.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';

import { GITHUB_DIR, loadWorkflow } from './lib/harness.mjs';

const MIN_COOLDOWN_DAYS = 7;

// The cooldown of each updates entry, named by its package ecosystem.
function cooldowns() {
  const config = loadWorkflow(join(GITHUB_DIR, 'dependabot.yml'));
  const updates = config?.updates;
  assert.ok(Array.isArray(updates) && updates.length > 0, 'dependabot.yml has no updates entries');
  return updates.map((entry, index) => [entry?.['package-ecosystem'] ?? `updates[${index}]`, entry?.cooldown]);
}

test('every ecosystem has a cooldown of at least 7 days', () => {
  const short = [];
  for (const [ecosystem, cooldown] of cooldowns()) {
    const days = cooldown?.['default-days'];
    if (typeof days !== 'number' || days < MIN_COOLDOWN_DAYS) {
      short.push(`${ecosystem}: cooldown default-days is ${JSON.stringify(days)}, want >= ${MIN_COOLDOWN_DAYS}`);
    }
  }
  assert.deepEqual(short, []);
});

// On an ecosystem with SemVer support, gomod among ours, a semver-*-days key
// replaces default-days for that update type, so a shorter one would undercut
// the policy. The keys are checked on every ecosystem: where Dependabot does
// not support them they have no effect, and a value under 7 would misstate
// the policy.
const SEMVER_DAYS = ['semver-major-days', 'semver-minor-days', 'semver-patch-days'];

test('no SemVer-specific cooldown is shorter than 7 days', () => {
  const short = [];
  for (const [ecosystem, cooldown] of cooldowns()) {
    for (const key of SEMVER_DAYS) {
      const days = cooldown?.[key];
      if (days !== undefined && (typeof days !== 'number' || days < MIN_COOLDOWN_DAYS)) {
        short.push(`${ecosystem}: cooldown ${key} is ${JSON.stringify(days)}, want >= ${MIN_COOLDOWN_DAYS}`);
      }
    }
  }
  assert.deepEqual(short, []);
});

// `include` limits the cooldown to the dependencies it lists unless it holds
// "*", and `exclude` lifts the cooldown from the ones it lists, even those
// `include` names. Either would let some updates open as soon as a release
// is out.
test('the cooldown applies to every dependency', () => {
  const narrowed = [];
  for (const [ecosystem, cooldown] of cooldowns()) {
    const include = cooldown?.include;
    if (include !== undefined && !(Array.isArray(include) && include.includes('*'))) {
      narrowed.push(`${ecosystem}: cooldown include is ${JSON.stringify(include)}, want it absent or holding "*"`);
    }
    const exclude = cooldown?.exclude;
    if (exclude !== undefined && !(Array.isArray(exclude) && exclude.length === 0)) {
      narrowed.push(`${ecosystem}: cooldown exclude is ${JSON.stringify(exclude)}, want it absent or empty`);
    }
  }
  assert.deepEqual(narrowed, []);
});
