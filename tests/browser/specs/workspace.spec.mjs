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

// A test can leave the shared development channel, so restore the membership
// precondition before every journey. Joining is idempotent.
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
  return postPayloadThroughTheAPI(request, body);
}

async function postPayloadThroughTheAPI(request, body) {
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
  await expect(composer).toHaveAttribute('maxlength', '40000');

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

  await page.locator('.result', { hasText: message }).click();
  await expect(page).toHaveURL(/#message-/);
  await expect(page.locator('.message-text', { hasText: message })).toBeVisible();
  await page.goBack();
  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.channel-title')).toHaveText('# general');

  await page.getByRole('link', { name: 'Members' }).click();
  await expect(page.getByRole('heading', { name: 'Workspace members' })).toBeVisible();
  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.channel-title')).toHaveText('# general');
});

test('JSON-authored blocks, attachments, and unfurls render as usable messages', async ({ page, context, request }) => {
  const stamp = Date.now();
  const blockHeader = `Deployment complete ${stamp}`;
  await postPayloadThroughTheAPI(request, {
    channel: CHANNEL,
    text: `notification fallback ${stamp}`,
    blocks: [
      { type: 'header', text: { type: 'plain_text', text: blockHeader } },
      {
        type: 'section',
        text: { type: 'mrkdwn', text: '*Production* is healthy' },
        fields: [{ type: 'plain_text', text: 'Region: eu-west' }],
      },
      {
        type: 'actions',
        elements: [{ type: 'button', text: { type: 'plain_text', text: 'View build' } }],
      },
    ],
  });
  const attachmentTitle = `Build 842 ${stamp}`;
  await postPayloadThroughTheAPI(request, {
    channel: CHANNEL,
    attachments: [
      {
        author_name: 'CI',
        title: attachmentTitle,
        title_link: 'https://example.com/build/842',
        text: 'All checks passed',
        fields: [{ title: 'Duration', value: '3m 12s' }],
        footer: 'Continuous delivery',
      },
    ],
  });
  const linkText = `runbook link ${stamp}`;
  const linked = await postThroughTheAPI(request, linkText);
  const unfurl = await request.post('/api/chat.unfurl', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: {
      channel: CHANNEL,
      ts: linked.ts,
      unfurls: {
        'https://example.com/runbook': {
          title: `Production runbook ${stamp}`,
          text: 'Recovery steps',
        },
      },
    },
  });
  const unfurlPayload = await unfurl.json();
  expect(unfurlPayload.ok, JSON.stringify(unfurlPayload)).toBe(true);

  await signIn(context);
  await page.goto('/app');

  const blockMessage = page.locator('.message', { hasText: blockHeader });
  await expect(blockMessage.getByText('*Production* is healthy', { exact: true })).toBeVisible();
  await expect(blockMessage.getByText('Region: eu-west', { exact: true })).toBeVisible();
  await expect(blockMessage.getByText('View build', { exact: true })).toBeVisible();
  await expect(blockMessage.locator('.message-text')).toHaveCount(0);
  await expect(blockMessage.getByText(`notification fallback ${stamp}`, { exact: true })).toHaveCount(0);
  await expect(blockMessage.getByText('Edit', { exact: true })).toHaveCount(0);
  await expect(blockMessage.getByText('Delete', { exact: true })).toBeVisible();

  const attachmentMessage = page.locator('.message', { hasText: attachmentTitle });
  await expect(attachmentMessage.getByText('All checks passed', { exact: true })).toBeVisible();
  await expect(attachmentMessage.locator('.attachment-fields')).toContainText('3m 12s');

  const unfurledMessage = page.locator('.message', { hasText: linkText });
  await expect(unfurledMessage.getByText(`Production runbook ${stamp}`, { exact: true })).toBeVisible();
  await expect(unfurledMessage.getByText('Recovery steps', { exact: true })).toBeVisible();
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

  // Slack's global search shortcut works from the composer, and Escape returns
  // to composing without submitting or losing text.
  await composer.fill('draft survives search');
  await composer.press('Control+k');
  const search = page.locator('#workspace-search');
  await expect(search).toBeFocused();
  await search.press('Escape');
  await expect(composer).toBeFocused();
  await expect(composer).toHaveValue('draft survives search');

  // Slash is the no-modifier search shortcut when the reader is not editing.
  await page.locator('.channel-title').click();
  await page.keyboard.press('/');
  await expect(search).toBeFocused();
});

test('message reading and actions honour Slack keyboard navigation', async ({ page, context, request }) => {
  await signIn(context);
  const first = await postThroughTheAPI(request, `keyboard first ${Date.now()}`);
  const second = await postThroughTheAPI(request, `keyboard second ${Date.now()}`);
  const last = await postThroughTheAPI(request, `keyboard last ${Date.now()}`);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  await composer.fill('');
  await composer.press('ArrowUp');
  const lastMessage = page.locator('.message', { hasText: `keyboard last` });
  const secondMessage = page.locator('.message', { hasText: `keyboard second` });
  await expect(lastMessage).toBeFocused();

  await page.keyboard.press('ArrowUp');
  await expect(secondMessage).toBeFocused();
  await page.keyboard.press('End');
  await expect(page.locator('#timeline .message').last()).toBeFocused();
  await page.keyboard.press('Home');
  await expect(page.locator('#timeline .message').first()).toBeFocused();

  await lastMessage.focus();
  await page.keyboard.press('p');
  await expect(lastMessage.locator('.pinned')).toBeVisible();
  await expect(lastMessage).toBeFocused();

  await page.keyboard.press('r');
  const reaction = lastMessage.locator('input[name="name"]');
  await expect(reaction).toBeFocused();
  await reaction.fill('wave');
  await reaction.press('Enter');
  await expect(lastMessage.locator('.reactions .chip')).toContainText('wave');

  await lastMessage.focus();
  await page.keyboard.press('e');
  const editor = lastMessage.getByRole('textbox', { name: 'Edit your message' });
  await expect(editor).toBeFocused();
  await editor.evaluate((node) => {
    node.closest('details').open = false;
    node.closest('.message').focus();
  });

  await page.keyboard.press('t');
  await expect(page).toHaveURL(new RegExp(`thread=${encodeURIComponent(last.ts)}`));
  const threadRoot = page.locator('#thread-messages .message').first();
  await threadRoot.focus();
  await page.keyboard.press('ArrowLeft');
  await expect(page).not.toHaveURL(/thread=/);

  const returned = page.locator('.message', { hasText: `keyboard last` });
  await returned.focus();
  await page.keyboard.press('Delete');
  await expect(returned.getByRole('button', { name: 'Delete this message' })).toBeFocused();

  // The earlier messages are intentionally referenced so the journey proves
  // arrow navigation follows chronology rather than a coincidental last row.
  expect(first.ts).not.toBe(second.ts);
});

// Public channels are intentionally readable before joining, but that does not
// make their mutation controls usable. The UI must offer the real membership
// transition first and reveal the composer only after it succeeds.
test('a public-channel preview can be joined and posted to', async ({ page, context, request }) => {
  const leave = await request.post('/api/conversations.leave', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL },
  });
  const left = await leave.json();
  expect(left.ok, JSON.stringify(left)).toBe(true);

  await signIn(context);
  await page.goto('/app');
  await expect(page.getByText('Not joined', { exact: true })).toBeVisible();
  await expect(page.locator('form.composer')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Pin' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Join channel' })).toBeVisible();

  await page.getByRole('button', { name: 'Join channel' }).click();
  await expect(page).toHaveURL(/\/app\?channel=Cdev/);
  const composer = page.locator('form.composer textarea[name="text"]');
  await expect(composer).toBeVisible();
  await expect(page.getByText('Joined', { exact: true })).toBeVisible();

  const sent = `joined from browser ${Date.now()}`;
  await composer.fill(sent);
  await composer.press('Enter');
  await expect(page.locator('.message-text').last()).toHaveText(sent);
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

  await target.hover();
  const reaction = target.locator('form[aria-label="Add reaction"] input[name="name"]');
  await target.getByText('Add reaction', { exact: true }).click();
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

test('a member can edit and delete their own message in place', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  const original = `edit target ${Date.now()}`;
  await composer.fill(original);
  await composer.press('Enter');

  const target = page.locator('.message', { hasText: original });
  await target.hover();
  await target.getByText('Edit', { exact: true }).click();
  const editor = target.getByRole('textbox', { name: 'Edit your message' });
  const changed = `edited in browser ${Date.now()}`;
  await editor.fill(changed);
  await target.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.message', { hasText: changed })).toHaveCount(1);
  await expect(page.locator('.message', { hasText: original })).toHaveCount(0);

  const changedTarget = page.locator('.message', { hasText: changed });
  await changedTarget.hover();
  await changedTarget.getByText('Delete', { exact: true }).click();
  await changedTarget.getByRole('button', { name: 'Delete this message' }).click();
  await expect(page.locator('.message', { hasText: changed })).toHaveCount(0);
});

test('channel creation is reachable and conversation shortcuts switch channels', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  await page.getByText('Add channel', { exact: false }).click();
  const name = `browser-${Date.now()}`;
  await page.getByLabel('Channel name').fill(name);
  await page.getByLabel('Private channel').check();
  await page.getByRole('button', { name: 'Create' }).click();

  await expect(page.locator('.channel-title')).toHaveText(`# ${name}`);
  const createdURL = page.url();

  // A duplicate is rejected in place: the entered name and the open form
  // survive, and the error belongs to the action rather than the composer.
  await page.getByText('Add channel', { exact: false }).click();
  await page.getByLabel('Channel name').fill(name);
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(page).toHaveURL(createdURL);
  await expect(page.getByLabel('Channel name')).toHaveValue(name);
  await expect(page.locator('#action-feedback')).toContainText('already exists');
  await expect(page.locator('#composer-error')).toBeHidden();

  await page.keyboard.press('Alt+ArrowUp');
  await expect(page).not.toHaveURL(createdURL);
  await page.keyboard.press('Alt+ArrowDown');
  await expect(page).toHaveURL(createdURL);
});

test('profile editing presents one human-facing photo field and saves status', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app/members');

  await expect(page.getByRole('heading', { name: 'People', level: 1 })).toBeVisible();
  await expect(page.locator('input[name^="image_"]')).toHaveCount(0);
  await expect(page.getByLabel('Profile photo URL')).toHaveCount(1);

  const status = `Qualifying ${Date.now()}`;
  await page.getByLabel('Status', { exact: true }).fill(status);
  await page.getByLabel('Status emoji').fill(':white_check_mark:');
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page).toHaveURL('/app/members');
  await expect(page.getByText(status, { exact: false }).first()).toBeVisible();
});

test('theme choice persists across workspace pages', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const toggle = page.getByRole('button', { name: 'Theme' });
  await toggle.click();
  const theme = await page.locator('html').getAttribute('data-theme');
  await page.getByRole('link', { name: 'Members' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', theme);
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

  // An unrelated action failure gets its own alert and cannot erase or replace
  // the send failure while the unsent draft is still present.
  const sendError = await error.textContent();
  await page.unroute('**/app/message*');
  const target = page.locator('.message').last();
  await page.route('**/app/pin*', (route) => route.abort());
  await target.getByRole('button', { name: 'Pin' }).click();
  const actionError = page.locator('#action-feedback');
  await expect(actionError).toBeVisible();
  await expect(actionError).not.toContainText('message was kept');
  await expect(error).toHaveText(sendError);
  await expect(composer).toHaveValue(doomed);
});

// The old 64px rail gave every channel the same # glyph and every DM the same @
// glyph. Accessible names did not help a sighted mobile reader choose one.
test('the narrow layout exposes named navigation and keeps the thread reachable', async ({ page, context, request }) => {
  await signIn(context);
  const root = await postThroughTheAPI(request, `narrow thread ${Date.now()}`);
  await page.setViewportSize({ width: 480, height: 900 });
  await page.goto(`/app?channel=${CHANNEL}&thread=${encodeURIComponent(root.ts)}`);

  const sidebar = page.locator('#workspace-sidebar');
  await expect(sidebar).toHaveAttribute('inert', '');
  const toggle = page.locator('#nav-toggle');
  await expect(toggle).toHaveAccessibleName('Open navigation');
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  await expect(toggle).toHaveAccessibleName('Close navigation');
  await expect(sidebar).toHaveClass(/is-open/);

  const links = sidebar.locator('.side-link');
  const count = await links.count();
  expect(count).toBeGreaterThan(0);
  for (let index = 0; index < count; index += 1) {
    const link = links.nth(index);
    const name = ((await link.getAttribute('aria-label')) ?? (await link.innerText())).trim();
    expect(name, `sidebar control ${index} has no accessible name`).not.toBe('');
  }
  await expect(sidebar.getByText('general', { exact: true })).toBeVisible();

  // The drawer is modal on narrow screens. Keyboard focus must not disappear
  // into the conversation hidden behind its scrim.
  const firstDrawerControl = sidebar.locator('a[href], button:not([disabled]), summary').first();
  await firstDrawerControl.focus();
  await page.keyboard.press('Shift+Tab');
  expect(await page.evaluate(() => document.querySelector('#workspace-sidebar').contains(document.activeElement))).toBe(true);

  await expect(page.getByRole('button', { name: 'Sign out' })).toHaveAttribute('aria-label', 'Sign out');
  // The thread reflows instead of disappearing.
  await expect(page.locator('#thread-messages')).toBeVisible();

  await page.getByText('Add channel', { exact: false }).click();
  const channelName = page.getByLabel('Channel name');
  await expect(channelName).toBeVisible();
  const panel = await channelName.boundingBox();
  expect(panel.x).toBeGreaterThanOrEqual(0);
  expect(panel.x + panel.width).toBeLessThanOrEqual(480);

  await page.keyboard.press('Escape');
  await expect(sidebar).not.toHaveClass(/is-open/);
  await expect(toggle).toBeFocused();
});

test('named mobile navigation survives without JavaScript', async ({ browser }) => {
  const context = await browser.newContext({
    baseURL: 'http://127.0.0.1:18080',
    javaScriptEnabled: false,
    viewport: { width: 480, height: 900 },
  });
  await signIn(context);
  const page = await context.newPage();
  const response = await page.goto('/app');
  expect(response.status()).toBe(200);

  await expect(page.locator('html')).not.toHaveClass(/js/);
  await expect(page.locator('#workspace-sidebar')).toBeVisible();
  await expect(page.locator('#workspace-sidebar').getByText('general', { exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Members' })).toBeVisible();
  await context.close();
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
  await expect(page.getByText('Ask a workspace administrator how to sign in again.')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Sign in with Shauth' })).toHaveCount(0);

  // The revoked session cannot reopen a protected page.
  const protectedResponse = await page.goto('/app');
  expect(protectedResponse.status()).toBe(401);
});
