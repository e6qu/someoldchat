import { test, expect } from '@playwright/test';

// The message surface as Slack presents it: how a message reads, its hover
// toolbar and More actions menu, in-place editing, the Delete and Forward
// dialogs, the emoji picker, images, and the Threads view. The contracts are
// MSG-*, THREAD-* and ACT-* in specs/journeys/04-messages-threads-and-actions.md.

const SESSION = 'browser-session';
const API_TOKEN = 'xoxb-browser';
const CHANNEL = 'Cdev';

async function signIn(context) {
  await context.addCookies([{ name: 'sameoldchat_session', value: SESSION, url: 'http://127.0.0.1:18080' }]);
}

test.beforeEach(async ({ request }) => {
  const response = await request.post('/api/conversations.join', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL },
  });
  expect((await response.json()).ok).toBe(true);
});

async function post(request, body) {
  const response = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL, ...body },
  });
  const payload = await response.json();
  expect(payload.ok, JSON.stringify(payload)).toBe(true);
  return payload;
}

function messageWith(page, text) {
  return page.locator('#timeline .message').filter({ has: page.locator('.message-text', { hasText: text }) });
}

test('[MSG-01] mrkdwn reads as Slack formats it: quotes, code blocks, lists, mentions and single line breaks', async ({ page, context, request }) => {
  await signIn(context);
  const stamp = Date.now();
  const auth = await (await request.post('/api/auth.test', { headers: { authorization: `Bearer ${API_TOKEN}` } })).json();
  await post(request, { text: `format ${stamp}\nsecond line\n>quoted one\n>quoted two\n• item one\n• item two\n\`\`\`\nfunc main() {}\n\`\`\`\nhey <@${auth.user_id}> and <!here>` });
  await post(request, { text: ':tada::rocket:' });
  await page.goto('/app');

  const message = messageWith(page, `format ${stamp}`);
  const body = message.locator('.message-text');
  await expect(body.locator('blockquote')).toHaveText('quoted onequoted two');
  await expect(body.locator('pre code')).toHaveText('func main() {}');
  await expect(body.locator('ul li')).toHaveText(['item one', 'item two']);
  // One line break per newline: the second line sits one line below the first.
  const lines = await body.evaluate((node) => {
    const range = document.createRange();
    const first = node.firstChild;
    range.selectNodeContents(first);
    const firstBox = range.getBoundingClientRect();
    const second = node.childNodes[2];
    range.selectNodeContents(second);
    return { gap: range.getBoundingClientRect().top - firstBox.top, lineHeight: parseFloat(getComputedStyle(node).lineHeight) };
  });
  expect(lines.gap).toBeLessThan(lines.lineHeight * 1.5);
  // The reader's own mention and @here are highlighted, and so is the row.
  await expect(message.locator('.slack-mention[data-self]')).toBeVisible();
  await expect(message.locator('.mention-broadcast')).toHaveText('@here');
  // The row highlight is for someone else mentioning you; this message is the
  // reader's own, so only the mention itself is marked.
  await expect(message).not.toHaveClass(/mentions-me/);
  await expect(message.locator('a.slack-mention[data-self]')).toHaveAttribute('href', /\/app\/members\?q=/);
  // An emoji-only message is enlarged.
  await expect(page.locator('#timeline .message-text.jumbo').last()).toBeVisible();
});

test('[MSG-01 ACT-01] consecutive messages group under one name, with the time on hover', async ({ page, context, request }) => {
  await signIn(context);
  const stamp = Date.now();
  // The head must start a group whatever the suite posted just before it.
  // It used to rely on the reader's own message counting as unread, which
  // put a "New" divider above it; a member's own messages are never unread,
  // so a channel notice (a topic change) breaks the run instead.
  const topic = await request.post('/api/conversations.setTopic', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL, topic: `grouping ${stamp}` },
  });
  expect((await topic.json()).ok).toBe(true);
  await post(request, { text: `group head ${stamp}` });
  await post(request, { text: `group tail ${stamp}` });
  await page.goto('/app');

  const head = messageWith(page, `group head ${stamp}`);
  const tail = messageWith(page, `group tail ${stamp}`);
  await expect(head.locator('.message-head .author')).toBeVisible();
  await expect(tail).toHaveClass(/is-continuation/);
  await expect(tail.locator('.message-head')).toBeHidden();
  await expect(tail.locator('.gutter-time')).toBeHidden();
  await tail.hover();
  await expect(tail.locator('.gutter-time')).toBeVisible();
  // The header time is the clock time, and its tooltip is the full date.
  await expect(head.locator('.message-head time')).toHaveText(/^\d{1,2}:\d{2}\s?(AM|PM)$/i);
  await expect(head.locator('.message-head time')).toHaveAttribute('title', /^\w+day, \w+ \d{1,2}(st|nd|rd|th) at \d{1,2}:\d{2}:\d{2}\s?(AM|PM)$/);
  await expect(page.locator('.day-separator .day-pill').last()).toHaveText('Today');
});

test('[ACT-01 A11Y-01] the toolbar and More actions menu follow Slack, by pointer, right-click and keyboard', async ({ page, context, request }) => {
  await signIn(context);
  const text = `menu target ${Date.now()}`;
  await post(request, { text });
  await page.goto('/app');
  const message = messageWith(page, text);
  await message.hover();

  const toolbar = message.getByRole('toolbar');
  const names = await toolbar.locator(':scope > button, :scope > a, :scope > form > button, :scope > details > summary').evaluateAll((nodes) => nodes.map((node) => node.getAttribute('aria-label')));
  expect(names.slice(0, 3).every((name) => /^React with :/.test(name))).toBe(true);
  expect(names.slice(3)).toEqual(['Add reaction', 'Reply in thread', 'Forward message', 'Save for later', 'More actions']);

  const more = message.locator('summary[aria-label="More actions"]');
  await more.focus();
  await page.keyboard.press('Enter');
  const menu = message.getByRole('menu', { name: 'More actions' });
  await expect(menu).toBeVisible();
  await expect(menu.getByRole('menuitem').first()).toBeFocused();
  await expect(menu.getByRole('menuitem')).toHaveText([
    /Mark unread\s*U/, /Remind me about this/, 'Copy link', /Pin to channel\s*P/, /Edit message\s*E/, /Delete message…\s*delete/,
  ]);
  await page.keyboard.press('ArrowDown');
  await expect(menu.getByRole('menuitem', { name: 'Remind me about this' })).toBeFocused();
  await page.keyboard.press('ArrowRight');
  const reminders = message.getByRole('menu', { name: 'Remind me about this' });
  await expect(reminders).toBeVisible();
  const remindersBox = await reminders.boundingBox();
  expect(remindersBox.x).toBeGreaterThanOrEqual(0);
  await expect(message.getByRole('menu', { name: 'Remind me about this' }).getByRole('menuitem')).toHaveText([
    'In 20 minutes', 'In 1 hour', 'In 3 hours', 'Tomorrow', 'Next week', 'Custom…',
  ]);
  await expect(message.getByRole('menuitem', { name: 'In 20 minutes' })).toBeFocused();
  await page.keyboard.press('ArrowLeft');
  await expect(menu.getByRole('menuitem', { name: 'Remind me about this' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(menu).toBeHidden();
  await expect(more).toBeFocused();

  // Right-click opens the same menu at the pointer; a click elsewhere closes it.
  const box = await message.locator('.message-text').boundingBox();
  await page.mouse.click(box.x + 10, box.y + 5, { button: 'right' });
  await expect(menu).toBeVisible();
  const placed = await menu.boundingBox();
  expect(Math.abs(placed.x - (box.x + 10))).toBeLessThan(2);
  await page.mouse.click(5, 5);
  await expect(menu).toBeHidden();
});

test('[ACT-01 RESPONSIVE-01] the More actions menu stays on screen at phone width', async ({ page, context, request }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await signIn(context);
  const text = `narrow menu ${Date.now()}`;
  await post(request, { text });
  await page.goto('/app');
  const message = messageWith(page, text);
  await message.locator('.message-text').click();
  await message.locator('summary[aria-label="More actions"]').click();
  const menu = message.getByRole('menu', { name: 'More actions' });
  await expect(menu).toBeVisible();
  const box = await menu.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(390);
  expect(box.y + box.height).toBeLessThanOrEqual(844);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test('[MSG-03] editing happens in place: Enter saves, Shift+Enter adds a line, Escape cancels', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  const composer = page.locator('#text-editor');
  const original = `in place ${Date.now()}`;
  await composer.fill(original);
  await composer.press('Enter');
  const message = messageWith(page, original);
  await expect(message).toHaveCount(1);

  await message.focus();
  await page.keyboard.press('e');
  const editor = message.getByRole('textbox', { name: 'Edit message' });
  await expect(editor).toBeFocused();
  await expect(editor).toHaveValue(original);
  await page.keyboard.type(' discarded');
  await page.keyboard.press('Escape');
  await expect(editor).toBeHidden();
  await expect(message).toBeFocused();
  await expect(message.locator('.message-text')).toHaveText(original);

  await page.keyboard.press('e');
  await page.keyboard.press('Shift+Enter');
  await page.keyboard.type('kept');
  await page.keyboard.press('Enter');
  const edited = messageWith(page, 'kept');
  await expect(edited.locator('.message-text')).toContainText(original);
  await expect(edited.locator('.edited-label')).toHaveText(/\(edited\)/);
  await expect(edited).toBeFocused();
});

test('[MSG-04 ACT-01] Delete asks with Slack\'s confirmation, quoting the message, and returns focus on cancel', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  const composer = page.locator('#text-editor');
  const text = `to delete ${Date.now()}`;
  await composer.fill(text);
  await composer.press('Enter');
  const message = messageWith(page, text);
  await message.focus();
  await page.keyboard.press('Delete');
  const dialog = page.getByRole('dialog', { name: 'Delete message' });
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveAccessibleDescription('Are you sure you want to delete this message? This cannot be undone.');
  await expect(dialog.locator('[data-dialog-preview]')).toContainText(text);
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(message).toBeFocused();

  await page.keyboard.press('Delete');
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click();
  await expect(messageWith(page, text)).toHaveCount(0);
});

test('[ACT-03] Forward message searches destinations, previews the message, and stays in place', async ({ page, context, request }) => {
  await signIn(context);
  const text = `to forward ${Date.now()}`;
  await post(request, { text });
  await page.goto('/app');
  const url = page.url();
  const message = messageWith(page, text);
  await message.hover();
  await message.getByRole('link', { name: 'Forward message' }).click();
  const dialog = page.getByRole('dialog', { name: 'Forward message' });
  await expect(dialog.getByPlaceholder('Search for channel or person')).toBeFocused();
  await expect(dialog.locator('[data-dialog-preview]')).toContainText(text);
  await dialog.getByPlaceholder('Search for channel or person').fill('gener');
  await expect(dialog.getByRole('listbox')).toHaveValue(CHANNEL);
  await dialog.getByLabel('Add a message').fill('have a look');
  await dialog.getByRole('button', { name: 'Forward', exact: true }).click();
  await expect(dialog).toBeHidden();
  await expect(page.locator('#message-toast')).toHaveText('Message forwarded to #general');
  await expect(page).toHaveURL(url);
  await expect(page.locator('#timeline .message', { hasText: 'have a look' })).toContainText('Forwarded from');
});

test('[ACT-02] the emoji picker browses categories, previews, and reacts; pills name who reacted', async ({ page, context, request }) => {
  await signIn(context);
  const text = `picker target ${Date.now()}`;
  await post(request, { text });
  await page.goto('/app');
  const message = messageWith(page, text);
  await message.hover();
  const trigger = message.locator('.message-actions').getByRole('button', { name: 'Add reaction' });
  await trigger.click();
  const picker = page.getByRole('dialog', { name: 'Emoji picker' });
  await expect(picker).toBeVisible();
  await expect(picker.getByPlaceholder('Search all emoji')).toBeFocused();
  // Anchored to the button that opened it, and on screen.
  const pickerBox = await picker.boundingBox();
  const viewport = page.viewportSize();
  expect(pickerBox.y + pickerBox.height).toBeLessThanOrEqual(viewport.height);
  await picker.getByRole('tab', { name: 'Animals & nature' }).click();
  await expect(picker.getByRole('tab', { name: 'Animals & nature' })).toHaveAttribute('aria-selected', 'true');
  expect(await picker.getByRole('option').count()).toBeGreaterThan(100);
  await picker.getByRole('option').first().hover();
  await expect(picker.locator('.emoji-preview-name')).toHaveText(/^:[a-z0-9_+-]+:$/);
  await page.keyboard.press('Escape');
  await expect(picker).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await picker.getByPlaceholder('Search all emoji').fill('thumbsup');
  await expect(picker.getByRole('option', { name: ':thumbsup:' })).toBeVisible();
  await page.keyboard.press('Enter');
  const pill = message.locator('.reactions .chip[aria-pressed="true"]');
  await expect(pill).toHaveAttribute('title', 'You reacted with :thumbsup:');
  // The toolbar now offers it as a one-click reaction.
  await page.reload();
  await messageWith(page, text).hover();
  await expect(messageWith(page, text).locator('.message-actions .quick-reaction').first()).toHaveAttribute('aria-label', 'React with :thumbsup:');
});

test('[THREAD-01 THREAD-02] the thread pane names its channel, counts replies, and labels broadcasts', async ({ page, context, request }) => {
  await signIn(context);
  const rootText = `pane root ${Date.now()}`;
  const root = await post(request, { text: rootText });
  await post(request, { text: 'first reply', thread_ts: root.ts });
  await post(request, { text: 'broadcast reply', thread_ts: root.ts, reply_broadcast: true });
  await page.goto('/app');

  const summary = messageWith(page, rootText).locator('.thread-summary');
  await expect(summary.locator('.thread-count')).toHaveText('2 replies');
  await expect(summary.locator('.thread-avatars .avatar')).toBeVisible();
  await expect(summary.locator('.thread-last-reply')).toContainText(/Last reply (just now|\d+ (second|minute)s? ago|now)/);
  const broadcast = messageWith(page, 'broadcast reply');
  await expect(broadcast.locator('.broadcast-context')).toContainText(`replied to a thread: ${rootText}`);

  await page.goto(`/app?channel=${CHANNEL}&thread=${encodeURIComponent(root.ts)}`);
  const pane = page.getByRole('complementary', { name: 'Thread' });
  await expect(pane.locator('.thread-channel')).toHaveText('#general');
  await expect(pane.locator('.thread-replies-divider')).toHaveText('2 replies');
  await expect(pane.locator('.message', { hasText: 'broadcast reply' }).locator('.broadcast-label')).toHaveText('Also sent to #general');
  await expect(pane.locator('.unread-divider')).toHaveCount(0);
  await pane.getByRole('link', { name: 'Close thread' }).click();
  await expect(page).not.toHaveURL(/thread=/);
});

test('[THREAD-01 COMP-02] a thread opens in place with its own reply composer, the shared emoji picker, and Up to edit', async ({ page, context, request }) => {
  await signIn(context);
  const rootText = `in place root ${Date.now()}`;
  await post(request, { text: rootText });
  await page.goto('/app');
  // A full page load would drop this marker; opening and closing the pane in
  // place keeps it.
  await page.evaluate(() => { window.__samePage = true; });

  const root = messageWith(page, rootText);
  await root.focus();
  await page.keyboard.press('t');
  const pane = page.getByRole('complementary', { name: 'Thread' });
  await expect(pane).toBeVisible();
  await expect(page).toHaveURL(/thread=/);
  expect(await page.evaluate(() => window.__samePage)).toBe(true);

  // The swapped-in pane's reply composer is live: it takes text, the one
  // emoji picker inserts into it rather than into the channel composer, and
  // it posts into the thread.
  const reply = pane.locator('#thread-text-editor');
  await reply.fill('reply in place');
  await reply.press('End');
  await pane.getByRole('button', { name: 'Emoji' }).click();
  const picker = page.getByRole('dialog', { name: 'Emoji picker' });
  await expect(picker).toBeVisible();
  await picker.getByPlaceholder('Search all emoji').fill('tada');
  await picker.getByRole('option', { name: ':tada:' }).first().click();
  await expect(picker).toBeHidden();
  await expect(page.locator('#thread-text')).toHaveValue('reply in place:tada:');
  await expect(page.locator('#text')).toHaveValue('');
  await reply.press('Enter');
  const sent = pane.locator('.message', { hasText: 'reply in place' });
  await expect(sent).toHaveCount(1);
  await expect(page.locator('.thread-summary .thread-count').first()).toBeVisible();

  // Up in the empty reply composer edits the member's own last reply in place.
  await reply.press('ArrowUp');
  const editor = sent.getByRole('textbox', { name: 'Edit message' });
  await expect(editor).toBeFocused();
  await editor.press('Escape');
  await expect(editor).toBeHidden();

  // Each composer keeps its own draft: an unsent reply stays with the thread
  // and never surfaces in the conversation's composer.
  await reply.fill('unsent reply draft');
  await expect(page.locator('#thread-text')).toHaveValue('unsent reply draft');
  const threadURL = page.url();
  await page.goto(threadURL);
  await expect(page.locator('#thread-text')).toHaveValue('unsent reply draft');
  await expect(page.locator('#text')).toHaveValue('');
  await page.locator('#thread-text-editor').fill('');
  await expect(page.locator('#thread-text')).toHaveValue('');
  await page.evaluate(() => { window.__samePage = true; });

  await page.getByRole('complementary', { name: 'Thread' }).getByRole('link', { name: 'Close thread' }).click();
  await expect(pane).toHaveCount(0);
  await expect(page).not.toHaveURL(/thread=/);
  expect(await page.evaluate(() => window.__samePage)).toBe(true);
  await expect(root).toBeFocused();
  await expect(page.locator('#text')).toHaveValue('');
});

test('[FILE-01 A11Y-01] an image opens in the viewer and Escape returns to it', async ({ page, context, request }) => {
  await signIn(context);
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');
  const headers = { authorization: `Bearer ${API_TOKEN}` };
  const ticket = await (await request.post('/api/files.getUploadURLExternal', { headers, form: { filename: 'pixel.png', length: String(png.length) } })).json();
  expect(ticket.ok, JSON.stringify(ticket)).toBe(true);
  await request.post(ticket.upload_url, { headers: { 'content-type': 'application/octet-stream' }, data: png });
  const title = `pixel ${Date.now()}`;
  const done = await (await request.post('/api/files.completeUploadExternal', { headers: { ...headers, 'content-type': 'application/json' }, data: { files: [{ id: ticket.file_id, title }], channel_id: CHANNEL } })).json();
  expect(done.ok, JSON.stringify(done)).toBe(true);
  await page.goto('/app');

  const link = page.locator('#timeline a[data-lightbox]').last();
  await link.scrollIntoViewIfNeeded();
  await link.click();
  const viewer = page.getByRole('dialog', { name: title });
  await expect(viewer).toBeVisible();
  await expect(viewer.getByRole('link', { name: 'Download' })).toHaveAttribute('href', /\/app\/files\//);
  await page.keyboard.press('Escape');
  await expect(viewer).toBeHidden();
  await expect(link).toBeFocused();
});

test('[NAV-07 THREAD-01] the Threads view lists threads with their latest replies and a reply box', async ({ page, context, request }) => {
  await signIn(context);
  const stamp = Date.now();
  await post(request, { text: `no replies ${stamp}` });
  const root = await post(request, { text: `*threaded* ${stamp}` });
  await post(request, { text: `only reply ${stamp}`, thread_ts: root.ts });
  await page.goto(`/app/threads?channel=${CHANNEL}`);
  const card = page.locator('.thread-card', { hasText: `threaded ${stamp}` });
  await expect(card.locator('.thread-card-message').first().locator('strong')).toHaveText('threaded');
  await expect(card.locator('.thread-card-message.reply')).toContainText(`only reply ${stamp}`);
  await expect(card.locator('.thread-card-summary')).toContainText('1 reply');
  await expect(card.getByRole('link', { name: 'Reply…' })).toHaveAttribute('href', new RegExp(`thread=${encodeURIComponent(root.ts)}`));
  await expect(page.locator('.thread-card', { hasText: `no replies ${stamp}` })).toHaveCount(0);
  await expect(page.locator('main')).not.toContainText('Jan 1, 00:00');
});
