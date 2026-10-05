import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';

// The canvas's collaborative text runs twice: in Go on the server
// (internal/crdt) and in the script the canvas page ships. Both replay the
// same vectors, generated from the Go side, so an op one makes is an op the
// other integrates the same way. This replays them against the script exactly
// as the page serves it, under the page's own content security policy.
const vectors = JSON.parse(readFileSync(new URL('../../../internal/crdt/testdata/conformance.json', import.meta.url), 'utf8'));

test('[CANVAS-02] the canvas page edits text the way the server does', async ({ page, context }) => {
  await context.addCookies([{ name: 'sameoldchat_session', value: 'browser-session', url: 'http://127.0.0.1:18080' }]);
  await page.goto('/app/canvases');
  await page.getByText('Create a canvas').click();
  await page.getByLabel('Name').fill(`Conformance ${Date.now()}`);
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page.locator('[data-canvas-editor]')).toBeVisible();

  const failures = await page.evaluate((vectors) => {
    const failures = [];
    const text = window.sameoldchatCanvasText;
    for (const c of vectors.cases) {
      for (const order of c.orders) {
        const doc = text.create();
        for (const index of order) doc.apply(c.ops[index]);
        if (doc.text() !== c.text || doc.pendingCount() !== 0) failures.push(`${c.name}: read ${JSON.stringify(doc.text())}`);
        if (JSON.stringify(doc.snapshot()) !== JSON.stringify(c.snapshot)) failures.push(`${c.name}: stored ${JSON.stringify(doc.snapshot())}`);
      }
      const loaded = text.load(c.snapshot);
      if (loaded.text() !== c.text || JSON.stringify(loaded.snapshot()) !== JSON.stringify(c.snapshot)) failures.push(`${c.name}: loaded ${JSON.stringify(loaded.text())}`);
    }
    for (const e of vectors.edits) {
      const doc = text.create();
      for (const op of e.ops) doc.apply(op);
      let produced;
      if (e.kind === 'insert') produced = [doc.insert(e.replica, e.position, e.text)];
      else if (e.kind === 'remove') produced = [doc.remove(e.position, e.length)];
      else produced = doc.replace(e.replica, e.text);
      if (JSON.stringify(produced) !== JSON.stringify(e.produced)) failures.push(`${e.name}: made ${JSON.stringify(produced)}`);
      if (doc.text() !== e.result) failures.push(`${e.name}: read ${JSON.stringify(doc.text())}`);
    }
    return failures;
  }, vectors);
  expect(failures).toEqual([]);
  expect(vectors.cases.length).toBeGreaterThan(0);
  expect(vectors.edits.length).toBeGreaterThan(0);
});
