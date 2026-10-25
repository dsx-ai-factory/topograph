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
// update, on every ecosystem. 7 days is the threshold zizmor's
// dependabot-cooldown audit applies by default.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { join } from 'node:path';

import { GITHUB_DIR, loadWorkflow } from './lib/harness.mjs';

const MIN_COOLDOWN_DAYS = 7;

test('every ecosystem has a cooldown of at least 7 days', () => {
  const config = loadWorkflow(join(GITHUB_DIR, 'dependabot.yml'));
  const updates = config?.updates;
  assert.ok(Array.isArray(updates) && updates.length > 0, 'dependabot.yml has no updates entries');
  const short = [];
  updates.forEach((entry, index) => {
    const ecosystem = entry?.['package-ecosystem'] ?? `updates[${index}]`;
    const days = entry?.cooldown?.['default-days'];
    if (typeof days !== 'number' || days < MIN_COOLDOWN_DAYS) {
      short.push(`${ecosystem}: cooldown default-days is ${JSON.stringify(days)}, want >= ${MIN_COOLDOWN_DAYS}`);
    }
  });
  assert.deepEqual(short, []);
});
