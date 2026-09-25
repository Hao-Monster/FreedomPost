import { readFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import test from 'node:test';

const read = (name) => readFileSync(new URL(`../.github/workflows/${name}`, import.meta.url), 'utf8');

test('production release cannot run on push or implicitly select an unbound revision', () => {
  const workflow = read('deploy.yml');
  assert.doesNotMatch(workflow, /^  push:/m);
  assert.match(workflow, /workflow_dispatch:[\s\S]*release_sha:[\s\S]*required: true/);
  assert.match(workflow, /if: github.ref == 'refs\/heads\/main'/);
  assert.match(workflow, /environment: production/);
  assert.match(workflow, /ref: \$\{\{ inputs.release_sha \}\}/);
  assert.match(workflow, /test "\$RELEASE_SHA" = "\$GITHUB_SHA"/);
  assert.equal((workflow.match(/git\/ref\/heads\/main/g) ?? []).length, 2);
});

test('campaign mutation is restricted to the production main environment', () => {
  const workflow = read('benefit-campaign.yml');
  assert.match(workflow, /if: github.ref == 'refs\/heads\/main'/);
  assert.match(workflow, /environment: production/);
});
