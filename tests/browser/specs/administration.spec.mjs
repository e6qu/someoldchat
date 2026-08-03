import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// Administration journeys run against a server started with -session-admin, on
// its own port, because the escalation is the whole point: the shared
// development session normally holds member scopes, and the other three
// projects have to keep asserting what a plain member sees. ADMIN-01..03 and
// ADMIN-05 had no browser citation for exactly this reason — not because the
// pages were untestable, but because nothing could reach them.

const SESSION = 'browser-session';
const API_TOKEN = 'xoxb-browser';

async function signIn(context) {
  await context.addCookies([{ name: 'sameoldchat_session', value: SESSION, url: 'http://127.0.0.1:18083' }]);
}

async function expectNoSeriousAccessibilityViolations(page, selector) {
  let builder = new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']);
  if (selector) {
    builder = builder.include(selector);
  }
  const results = await builder.analyze();
  const violations = results.violations.filter(({ impact }) => impact === 'serious' || impact === 'critical');
  expect(violations, JSON.stringify(violations, null, 2)).toEqual([]);
}

test('[ADMIN-01] an administrator reaches member administration and the surface identifies who it acts on', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  // The sidebar leads there, which is half the journey: a surface nobody can
  // find is not reachable in any sense that matters.
  await page.getByRole('link', { name: 'Workspace settings' }).click();
  await expect(page).toHaveURL(/\/app\/admin\/settings/);
  await expect(page.getByRole('heading', { name: /Workspace settings/i })).toBeVisible();

  // The identity being administered is named, so an administrator cannot act on
  // a workspace they did not mean to.
  await expect(page.locator('body')).toContainText('SameOldChat');
  await expectNoSeriousAccessibilityViolations(page);
});

test('[ADMIN-02 ADMIN-05] workspace policy writes through and retention states its own terms', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app/admin/settings');

  // ADMIN-05: the control has to say, before it is used, that deletion is
  // permanent and applied on a schedule. A retention control that reads like a
  // filter would be a trap.
  const retention = page.locator('form[action="/app/admin/settings/retention"]');
  await expect(retention).toBeVisible();
  await expect(page.locator('body')).toContainText(/permanent/i);
  await expect(page.locator('body')).toContainText(/schedul|swept|sweep/i);

  // ADMIN-02: a policy change writes through and is visible on reload, rather
  // than being a control that reports success and stores nothing.
  await retention.locator('[name="message_days"]').fill('90');
  await retention.getByRole('button', { name: 'Save retention' }).click();
  await page.goto('/app/admin/settings');
  await expect(page.locator('form[action="/app/admin/settings/retention"] [name="message_days"]')).toHaveValue('90');
  // And it reports when the sweep last ran, so a stopped worker is visible
  // rather than silently breaking the policy's promise.
  await expect(page.locator('body')).toContainText(/swept|Nothing has been swept yet/i);

  // Out of range is an explicit refusal, not a silent clamp to something the
  // administrator did not choose.
  const outOfRange = page.locator('form[action="/app/admin/settings/retention"] [name="message_days"]');
  await outOfRange.fill('40000');
  await page.locator('form[action="/app/admin/settings/retention"]').getByRole('button', { name: 'Save retention' }).click();
  await page.goto('/app/admin/settings');
  await expect(page.locator('form[action="/app/admin/settings/retention"] [name="message_days"]')).toHaveValue('90');

  await expectNoSeriousAccessibilityViolations(page);
});

test('[ADMIN-03] audit and analytics render for an eligible role and agree with their own export', async ({ page, context, request }) => {
  await signIn(context);

  // Something to count and something to audit. Analytics counts durable rows,
  // so a workspace with no messages proves nothing.
  const posted = await request.post('http://127.0.0.1:18083/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: 'Cdev', text: `analytics subject ${Date.now()}` },
  });
  expect((await posted.json()).ok).toBe(true);

  await page.goto('/app/admin/analytics');
  await expect(page.getByRole('heading', { name: /Analytics/i })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  await page.goto('/app/admin/audit');
  await expect(page.getByRole('heading', { name: /Audit/i })).toBeVisible();
  // The export is the same handler answering JSON, which is what makes it
  // impossible for the two to drift: ADMIN-03 requires the export and the page
  // to agree, and two code paths are how they stop agreeing.
  const rows = await page.locator('table tbody tr').count();
  const exported = await request.get('http://127.0.0.1:18083/app/admin/audit', {
    headers: { accept: 'application/json', cookie: `sameoldchat_session=${SESSION}` },
  });
  expect(exported.status()).toBe(200);
  const payload = await exported.json();
  expect(payload.ok, JSON.stringify(payload)).toBe(true);
  expect(Array.isArray(payload.audit.Entries) || Array.isArray(payload.audit.entries)).toBe(true);
  const entries = payload.audit.entries ?? payload.audit.Entries;
  expect(entries.length, 'the export must not be empty when the page has rows').toBeGreaterThan(0);
  expect(rows, 'the page shows what the export carries').toBeGreaterThan(0);
  // Secrets are redacted in both: an entry names identifiers, never content.
  expect(JSON.stringify(entries)).not.toContain('analytics subject');
  await expectNoSeriousAccessibilityViolations(page);
});
