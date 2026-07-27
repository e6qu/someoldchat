import { test, expect } from '@playwright/test';

const SESSION = 'browser-session';
const API_TOKEN = 'xoxb-browser';
const CHANNEL = 'Cdev';

async function signIn(context) {
  await context.addCookies([
    {
      name: 'sameoldchat_session',
      value: SESSION,
      url: 'http://127.0.0.1:18080',
    },
  ]);
}

// The workspace channel has to be joined before anything can be posted into it:
// chat.postMessage now requires membership of the conversation it names, and the
// development seed in cmd/server (seedDevelopmentCredentials, cmd/server/main.go)
// creates the session and the API token without joining either to Cdev. Joining
// is idempotent, so this runs before every test and states the precondition the
// suite depends on instead of assuming it.
test.beforeEach(async ({ request }) => {
  const response = await request.post('/api/conversations.join', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL },
  });
  const payload = await response.json();
  expect(payload.ok, JSON.stringify(payload)).toBe(true);
});

// Posts through the Slack-compatible API rather than the interface, so a test
// can observe what the browser does with an event it did not itself originate.
async function postThroughTheAPI(request, text, threadTimestamp) {
  const body = { channel: CHANNEL, text };
  if (threadTimestamp) {
    body.thread_ts = threadTimestamp;
  }
  const response = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: body,
  });
  const payload = await response.json();
  expect(payload.ok, JSON.stringify(payload)).toBe(true);
  return payload;
}

test('workspace supports the core browser journey', async ({ page, context }) => {
  await signIn(context);

  await page.goto('/app');
  // The header, the document title and the composer placeholder name the
  // conversation. They rendered the conversation identifier before, so this
  // page was titled "# Cdev" while the sidebar said "general".
  await expect(page.locator('.channel-title')).toHaveText('# general');
  await expect(page).toHaveTitle(/#general/);

  const composer = page.locator('form.composer textarea[name="text"]');
  await expect(composer).toHaveAttribute('placeholder', /general/);

  const message = `browser qualification ${Date.now()}`;
  await composer.fill(message);
  const postMessage = page.waitForResponse((response) =>
    response.url().includes('/app/message') && response.request().method() === 'POST',
  );
  await page.getByRole('button', { name: 'Send' }).click();
  const postResponse = await postMessage;
  expect(postResponse.status(), await postResponse.text()).toBe(200);
  await expect(page.locator('.message-text').last()).toHaveText(message);

  // The composer is cleared after a successful send. It kept its text before,
  // so pressing Send twice posted the same message twice.
  await expect(composer).toHaveValue('');

  // Authors and avatars carry names and initials, not raw user identifiers.
  await expect(page.locator('.message').last().locator('.author')).not.toHaveText(/^U[A-Za-z0-9]+$/);
  await expect(page.locator('.message').last().locator('.avatar')).toHaveText(/^\S$/);

  const search = page.locator('form.search input[name="q"]');
  await search.fill('browser qualification');
  await search.press('Enter');
  await expect(page).toHaveURL(/\/app\/search\?/);
  await expect(page.getByRole('heading', { name: 'Search results' })).toBeVisible();
  await expect(page.locator('.result').last()).toContainText(message);

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.channel-title')).toHaveText('# general');

  await page.getByRole('link', { name: 'Members' }).click();
  await expect(page.getByRole('heading', { name: 'Workspace members' })).toBeVisible();
  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.channel-title')).toHaveText('# general');
});

// Every thread view answered `503 page rendering unavailable`, because the
// message partial resolved `$.CSRFToken` against a type that had no such field.
// Nothing in either suite opened a thread, so the whole feature was broken in a
// released product without a failing test.
test('opening a thread renders the thread and its composer', async ({ page, context, request }) => {
  await signIn(context);
  const root = await postThroughTheAPI(request, `thread root ${Date.now()}`);

  await page.goto('/app');
  const reply = page.locator('.message').last().getByRole('link', { name: 'Reply in thread' });
  const target = await reply.getAttribute('href');
  // Navigating to the reply link's own target asserts the status directly. This
  // response was 503 for every thread in the workspace.
  const threadResponse = await page.goto(target);
  expect(threadResponse.status()).toBe(200);

  const thread = page.locator('#thread-messages');
  await expect(thread).toBeVisible();
  await expect(page.locator('#thread-heading')).toBeVisible();

  // The reply composer must carry the CSRF field whose absence caused the
  // original failure, and a reply must actually post.
  const threadComposer = page.locator('form.composer textarea[name="text"]');
  await expect(page.locator('form.composer input[name="_csrf"]')).toHaveCount(1);
  await expect(page.locator('form.composer input[name="thread_ts"]')).toHaveValue(root.ts);

  const replyText = `thread reply ${Date.now()}`;
  await threadComposer.fill(replyText);
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(thread.locator('.message-text').last()).toHaveText(replyText);
});

// The composer advertised "Enter to send · Shift+Enter for a new line" and no
// keydown handler existed anywhere, so Enter only inserted a newline.
test('the composer honours the keyboard contract it advertises', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  const continued = `first line ${Date.now()}`;
  await composer.fill(continued);
  await composer.press('Shift+Enter');
  await composer.pressSequentially('second line');
  await expect(composer).toHaveValue(`${continued}\nsecond line`);

  await composer.fill('');
  const sent = `enter to send ${Date.now()}`;
  await composer.fill(sent);
  await composer.press('Enter');
  await expect(page.locator('.message-text').last()).toHaveText(sent);
  await expect(composer).toHaveValue('');
});

// The page closed its EventSource on the first submit and never reopened it, and
// separately suppressed every event while the autofocused composer held focus —
// so in the default state live delivery never reached the timeline at all.
test('live delivery keeps reaching the timeline after posting', async ({ page, context, request }) => {
  await signIn(context);
  await page.goto('/app');

  // Post from the interface first: this is the submit that used to close the
  // stream permanently.
  const composer = page.locator('form.composer textarea[name="text"]');
  const own = `own message ${Date.now()}`;
  await composer.fill(own);
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.locator('.message-text').last()).toHaveText(own);

  // Now originate an event elsewhere. It must arrive without a reload, and the
  // draft in the composer must survive the update.
  const draft = 'a draft that must survive';
  await composer.fill(draft);
  const elsewhere = `delivered live ${Date.now()}`;
  await postThroughTheAPI(request, elsewhere);

  await expect(page.locator('.message-text').last()).toHaveText(elsewhere, { timeout: 15_000 });
  await expect(composer).toHaveValue(draft);
});

// Reactions and pins persisted but were never rendered, and every mutation
// answered with a redirect that dropped the open thread and history position.
test('reactions and pins render and reverse in place', async ({ page, context, request }) => {
  await signIn(context);
  await postThroughTheAPI(request, `reaction target ${Date.now()}`);
  await page.goto('/app');

  const target = page.locator('.message').last();
  const url = page.url();

  const reaction = target.locator('form[aria-label="Add reaction"] input[name="name"]');
  await reaction.fill('wave');
  await reaction.press('Enter');

  const chip = target.locator('.reactions .chip');
  await expect(chip).toHaveCount(1);
  await expect(chip.first()).toContainText('wave');
  await expect(chip.first()).toHaveAttribute('aria-pressed', 'true');
  // The mutation must not navigate: it used to answer HX-Redirect and lose the
  // current view.
  await expect(page).toHaveURL(url);

  await chip.first().click();
  await expect(target.locator('.reactions .chip')).toHaveCount(0);

  await target.getByRole('button', { name: 'Pin' }).click();
  await expect(target.locator('.pinned')).toBeVisible();
  await target.getByRole('button', { name: 'Unpin' }).click();
  await expect(target.locator('.pinned')).toHaveCount(0);
});

// A failed post added a class no stylesheet defined, so the failure was
// invisible and the typed message was lost with no explanation.
test('a rejected post explains itself and keeps the draft', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  // Make the next mutation fail at the transport, which is the case that was
  // silent: the server never sees a valid request.
  await page.route('**/app/message*', (route) => route.abort());

  const composer = page.locator('form.composer textarea[name="text"]');
  const doomed = `never delivered ${Date.now()}`;
  await composer.fill(doomed);
  await page.getByRole('button', { name: 'Send' }).click();

  const error = page.locator('#composer-error');
  await expect(error).toBeVisible();
  await expect(error).toHaveAttribute('role', 'alert');
  await expect(error).not.toBeEmpty();
  await expect(composer).toHaveValue(doomed);
});

// The narrow layout hid both the icon and the label of every sidebar control,
// leaving unnamed empty boxes, and removed the thread pane outright rather than
// reflowing it.
test('the narrow layout keeps every control named and the thread reachable', async ({ page, context, request }) => {
  await signIn(context);
  const root = await postThroughTheAPI(request, `narrow thread ${Date.now()}`);
  await page.setViewportSize({ width: 480, height: 900 });
  await page.goto(`/app?channel=${CHANNEL}&thread=${encodeURIComponent(root.ts)}`);

  const links = page.locator('.sidebar .side-link');
  const count = await links.count();
  expect(count).toBeGreaterThan(0);
  for (let index = 0; index < count; index += 1) {
    const link = links.nth(index);
    const name = ((await link.getAttribute('aria-label')) ?? (await link.innerText())).trim();
    expect(name, `sidebar control ${index} has no accessible name`).not.toBe('');
  }

  await expect(page.getByRole('button', { name: 'Sign out' })).toHaveAttribute('aria-label', 'Sign out');
  // The thread reflows instead of disappearing.
  await expect(page.locator('#thread-messages')).toBeVisible();
});

// `GET /app` is the only entry point; a template edit used to be able to take it
// down entirely, because the handler rewrote its own rendered HTML afterwards
// and returned 503 when the expected substring was not found exactly once.
test('the workspace entry point renders without post-processing its own markup', async ({ page, context }) => {
  await signIn(context);
  const response = await page.goto('/app');
  expect(response.status()).toBe(200);

  // The search control is a real form from the template, not a label patched
  // into one after rendering.
  const form = page.locator('form.search');
  await expect(form).toHaveAttribute('method', 'get');
  await expect(form).toHaveAttribute('action', '/app/search');
  await expect(page.locator('label.search')).toHaveCount(0);
  await expect(page.locator('input[name="q"]')).toHaveCount(1);
});

// The workspace rendered no security headers at all, so it was framable — one
// click in an invisible frame lands on Sign out or Pin — and it was stored by
// the browser with a live CSRF token in it. The policy is also the only thing
// that can silently disable the whole client, so this asserts the page runs
// clean under it.
test('the workspace is protected and its own scripts run under its policy', async ({ page, context }) => {
  await signIn(context);

  const violations = [];
  page.on('console', (message) => {
    if (message.type() === 'error' && /content security policy/i.test(message.text())) {
      violations.push(message.text());
    }
  });

  const response = await page.goto('/app');
  expect(response.status()).toBe(200);
  const headers = response.headers();
  expect(headers['x-frame-options']).toBe('DENY');
  expect(headers['x-content-type-options']).toBe('nosniff');
  expect(headers['referrer-policy']).toBe('no-referrer');
  expect(headers['cache-control']).toBe('no-store');
  expect(headers['content-security-policy']).toContain("frame-ancestors 'none'");

  // The client runs: the composer submits through fetch rather than navigating,
  // which is only true if the inline script was allowed to execute.
  const composer = page.locator('form.composer textarea[name="text"]');
  const sent = `policy check ${Date.now()}`;
  await composer.fill(sent);
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.locator('.message-text').last()).toHaveText(sent);
  await expect(page).toHaveURL(/\/app$/);
  expect(violations, violations.join('\n')).toHaveLength(0);
});

// Nothing stopped a second submit: the button was never disabled and there was
// no in-flight flag, so a held Enter or a double click on a slow link posted the
// same message twice.
test('a second submit while the first is in flight posts one message', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  await page.route('**/app/message*', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 700));
    await route.continue();
  });

  const composer = page.locator('form.composer textarea[name="text"]');
  const once = `posted once ${Date.now()}`;
  await composer.fill(once);
  await page.getByRole('button', { name: 'Send' }).click();
  await composer.press('Enter');
  await page.getByRole('button', { name: 'Send' }).click({ force: true });

  await expect(page.locator('.message-text', { hasText: once })).toHaveCount(1);
  await page.unroute('**/app/message*');
  // The lock is released: the composer still works afterwards.
  const after = `still working ${Date.now()}`;
  await composer.fill(after);
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page.locator('.message-text').last()).toHaveText(after);
});

// Sending from a window of older history used to store the message and then
// refresh the historical window over it: the message appeared for a moment and
// vanished, and the reader had no way to know it had been sent.
test('a message sent while reading older history is not lost', async ({ page, context, request }) => {
  await signIn(context);
  for (let index = 0; index < 55; index += 1) {
    await postThroughTheAPI(request, `history filler ${index} ${Date.now()}`);
  }

  await page.goto('/app');
  await page.getByRole('link', { name: 'Show older messages' }).click();
  await expect(page.locator('#timeline')).toHaveAttribute('data-live', 'false');

  const composer = page.locator('form.composer textarea[name="text"]');
  const sent = `sent from history ${Date.now()}`;
  await composer.fill(sent);
  await page.getByRole('button', { name: 'Send' }).click();

  // The reader is taken to the window the message is actually in.
  await expect(page.locator('#timeline')).toHaveAttribute('data-live', 'true');
  await expect(page.locator('.message-text', { hasText: sent })).toHaveCount(1);
});

// Sign-out must come last in this file. The suite runs with a single worker
// against one server holding one static browser session, so revoking it ends
// every session the remaining tests would use. Placing this earlier makes every
// later test fail with 401 for a reason that has nothing to do with what it
// asserts.
test('signing out ends the session and the signed-out page is terminal', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  await page.getByRole('button', { name: 'Sign out' }).click();
  await expect(page).toHaveURL('/signed-out');
  await expect(page.getByRole('heading', { name: 'You’re signed out' })).toBeVisible();

  // The signed-out destination survives a reload rather than bouncing back into
  // an authenticated view.
  await page.reload();
  await expect(page).toHaveURL('/signed-out');
  await expect(page.getByRole('link', { name: 'Sign in with Shauth' })).toBeVisible();

  // The revoked session cannot reopen a protected page.
  const protectedResponse = await page.goto('/app');
  expect(protectedResponse.status()).toBe(401);
});
