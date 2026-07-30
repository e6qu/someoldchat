import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

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

async function slackModifiers(page) {
  const apple = await page.evaluate(() => /Mac|iPhone|iPad/.test(navigator.platform || ''));
  return {
    primary: apple ? 'Meta' : 'Control',
    activity: apple ? 'Control+3' : 'Control+Shift+3',
  };
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
  return postPayloadWithToken(request, API_TOKEN, body);
}

async function postPayloadWithToken(request, token, body) {
  const response = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
    data: body,
  });
  const payload = await response.json();
  expect(payload.ok, JSON.stringify(payload)).toBe(true);
  return payload;
}

async function installActivityBot(page, request) {
  await page.goto('/app/developer/apps');
  const redirectURI = 'https://client.example/browser-oauth-callback';
  const name = `Activity bot ${Date.now()}`;
  await page.getByRole('textbox', { name: 'App manifest (JSON)' }).fill(JSON.stringify({
    display_information: { name },
    oauth_config: {
      redirect_urls: [redirectURI],
      scopes: { bot: ['channels:join', 'chat:write'] },
    },
  }, null, 2));
  await page.getByRole('button', { name: 'Create app' }).click();
  await expect(page.getByRole('heading', { name: 'Save these app credentials now' })).toBeVisible();
  const appID = new URL(page.url()).searchParams.get('app');
  expect(appID).toBeTruthy();
  const clientID = (await page.locator('dt:text-is("Client ID") + dd code').textContent()).trim();
  const clientSecret = (await page.locator('dt:text-is("Client secret") + dd code').textContent()).trim();

  await page.getByRole('link', { name: 'Open install flow' }).click();
  await expect(page.getByRole('heading', { name: `Authorize ${name}` })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Allow' })).toBeEnabled();
  const consent = await page.locator('form').evaluate((form) => Object.fromEntries(new FormData(form)));
  consent.decision = 'approve';
  // Keep the external HTTPS callback from becoming a browser/network
  // dependency. The POST is the exact consent form the browser rendered, with
  // its live CSRF token and session; stopping at the redirect lets the test
  // exchange the authorization code itself.
  const authorizationResponse = await request.post('/oauth/v2/authorize', {
    headers: { cookie: `sameoldchat_session=${SESSION}` },
    form: consent,
    maxRedirects: 0,
  });
  expect(authorizationResponse.status()).toBe(302);
  const code = new URL(authorizationResponse.headers().location).searchParams.get('code');
  expect(code).toBeTruthy();

  const exchange = await request.post('/api/oauth.v2.access', {
    form: {
      client_id: clientID,
      client_secret: clientSecret,
      code,
      redirect_uri: redirectURI,
    },
  });
  const installed = await exchange.json();
  expect(installed.ok, JSON.stringify(installed)).toBe(true);
  expect(installed.access_token).toMatch(/^xoxb-/);
  expect(installed.bot_user_id).toBeTruthy();

  const join = await request.post('/api/conversations.join', {
    headers: { authorization: `Bearer ${installed.access_token}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL },
  });
  const joined = await join.json();
  expect(joined.ok, JSON.stringify(joined)).toBe(true);
  return { appID, token: installed.access_token };
}

async function expectNoSeriousAccessibilityViolations(page) {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
    .analyze();
  const violations = results.violations.filter(({ impact }) => impact === 'serious' || impact === 'critical');
  expect(violations, JSON.stringify(violations, null, 2)).toEqual([]);
}

test('[AUTH-01 MSG-01 COMP-01 SEARCH-01] workspace supports the core browser journey', async ({ page, context }) => {
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

test('[ACTIVITY-01 ACTIVITY-02 ACTIVITY-03 A11Y-01] Activity persists real app mentions and supports keyboard triage', async ({ page, context, request }) => {
  await signIn(context);
  let activityAppID;
  try {
    const selfMessage = `self activity exclusion ${Date.now()}`;
    await postThroughTheAPI(request, selfMessage);
    const activityBot = await installActivityBot(page, request);
    activityAppID = activityBot.appID;
    const first = `app mention one ${Date.now()}`;
    const second = `app mention two ${Date.now()}`;
    await postPayloadWithToken(request, activityBot.token, { channel: CHANNEL, text: `<@Udev> ${first}` });
    await postPayloadWithToken(request, activityBot.token, { channel: CHANNEL, text: `<@Udev> ${second}` });
    await page.goto('/app/activity');

    await expect(page.getByRole('heading', { name: 'Activity', exact: true, level: 2 })).toBeVisible();
    await expect(page.getByRole('link', { name: 'All' })).toHaveAttribute('aria-current', 'page');
    await expect(page.getByText(selfMessage)).toHaveCount(0);
    await page.getByRole('link', { name: 'Apps' }).click();
    await expect(page).toHaveURL(/kind=app/);
    await expect(page.getByRole('link', { name: 'Apps' })).toHaveAttribute('aria-current', 'page');
    await expect(page.locator('[data-activity-row]', { hasText: first })).toBeVisible();
    await expect(page.locator('[data-activity-row]', { hasText: second })).toBeVisible();

    await page.getByRole('button', { name: 'Dense' }).click();
    await expect(page.locator('.activity-list')).toHaveClass(/dense/);
    await expect(page.getByRole('button', { name: 'Dense' })).toHaveAttribute('aria-pressed', 'true');
    await page.reload();
    await expect(page.getByRole('button', { name: 'Dense' })).toHaveAttribute('aria-pressed', 'true');

    let rows = page.locator('[data-activity-row]');
    await expect(rows).toHaveCount(2);
    await rows.nth(0).focus();
    await page.keyboard.press('ArrowDown');
    await expect(rows.nth(1)).toBeFocused();
    await page.keyboard.press('x');
    await expect(rows.nth(1).locator('input[type=checkbox]')).toBeChecked();
    await expect(page.getByText('1 selected')).toBeVisible();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/thread=/);
    await expect(page.locator('form.composer textarea[name="text"]')).toHaveAttribute('placeholder', 'Reply in the thread');

    await page.goBack();
    rows = page.locator('[data-activity-row]');
    await rows.nth(0).focus();
    await page.keyboard.press('r');
    await expect(rows.nth(0)).not.toHaveClass(/unread/);
    await rows.nth(0).focus();
    await page.keyboard.press('c');
    await expect(page.locator('[data-activity-row]')).toHaveCount(1);

    await page.getByRole('link', { name: 'Unread' }).click();
    await expect(page.getByRole('link', { name: 'Unread' })).toHaveAttribute('aria-current', 'page');
    // Opening the source thread advances the conversation read cursor, so the
    // sibling notification is no longer allowed to stay unread.
    await expect(page.getByText('You’re all caught up.')).toBeVisible();
    await page.getByRole('link', { name: 'Cleared' }).click();
    await expect(page.locator('[data-activity-row]')).toHaveCount(1);
    await page.getByRole('button', { name: 'Restore this activity' }).click();
    await expect(page.getByText('No cleared activity.')).toBeVisible();
    await expectNoSeriousAccessibilityViolations(page);
  } finally {
    if (activityAppID) {
      await page.goto(`/app/developer/apps?app=${encodeURIComponent(activityAppID)}`);
      const remove = page.getByRole('button', { name: 'Delete app' });
      if (await remove.isVisible()) {
        await remove.click();
      }
    }
  }
});

test('[NOTIFY-01 NOTIFY-02 NOTIFY-03 THREAD-02 A11Y-01] notification preferences, DND, exceptions, and thread follows persist', async ({ page, context, request }) => {
  await signIn(context);
  await page.goto('/app');
  await page.goto(`/app/notifications?channel=${CHANNEL}`);
  await expect(page.getByRole('heading', { name: 'Notification preferences' })).toBeVisible();

  await page.getByLabel('Notify me about').selectOption('all');
  await page.getByLabel('Channel keywords').fill('release, customer escalation, RELEASE');
  await page.getByLabel('Show channels set to All new posts in Activity').check();
  await page.getByLabel('Show due personal reminders in Activity').uncheck();
  await page.getByRole('button', { name: 'Save workspace defaults' }).click();
  await expect(page.getByRole('status')).toHaveText('Notification preferences saved.');
  await expect(page.getByLabel('Notify me about')).toHaveValue('all');
  await expect(page.getByLabel('Channel keywords')).toHaveValue('customer escalation, release');
  await expect(page.getByLabel('Show due personal reminders in Activity')).not.toBeChecked();

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await page.getByRole('link', { name: 'Open conversation details' }).click();
  await page.getByLabel('Notify me about').selectOption('mute');
  await page.getByLabel('Follow every thread').check();
  await page.getByRole('button', { name: 'Save notifications' }).click();
  await expect(page).toHaveURL(/details=1#conversation-notifications$/);
  await expect(page.getByLabel('Notify me about')).toHaveValue('mute');
  await expect(page.getByLabel('Follow every thread')).toBeChecked();

  await page.goto(`/app/notifications?channel=${CHANNEL}`);
  const exception = page.getByRole('link', { name: /#general/ });
  await expect(exception).toContainText('mute');
  await expect(exception).toContainText('following every thread');

  await page.getByLabel('Custom minutes (optional)').fill('1');
  await page.getByRole('button', { name: 'Pause notifications' }).click();
  await expect(page.getByRole('status')).toHaveText('Notifications paused. Messages and Activity remain available.');
  await expect(page.getByRole('button', { name: 'Resume notifications' })).toBeVisible();
  await page.getByRole('button', { name: 'Resume notifications' }).click();
  await expect(page.getByRole('status')).toHaveText('Notifications resumed.');

  // Turn off the all-thread channel setting so the per-thread control can
  // demonstrate both states independently.
  await exception.click();
  await page.getByLabel('Follow every thread').uncheck();
  await page.getByRole('button', { name: 'Save notifications' }).click();
  const root = await postThroughTheAPI(request, `thread follow browser qualification ${Date.now()}`);
  await page.goto(`/app?channel=${CHANNEL}&thread=${encodeURIComponent(root.ts)}`);
  await expect(page.getByRole('button', { name: 'Following' })).toHaveAttribute('aria-pressed', 'true');
  await page.getByRole('button', { name: 'Following' }).click();
  await expect(page.getByRole('button', { name: 'Follow thread' })).toHaveAttribute('aria-pressed', 'false');
  await page.getByRole('button', { name: 'Follow thread' }).click();
  await expect(page.getByRole('button', { name: 'Following' })).toHaveAttribute('aria-pressed', 'true');
  await expectNoSeriousAccessibilityViolations(page);
});

test('[SCHED-01 SCHED-02 A11Y-01] scheduled send persists, stays out of history, and can be cancelled', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const message = `scheduled browser qualification ${Date.now()}`;
  const composer = page.locator('form.composer textarea[name="text"]');
  await composer.fill(message);
  await page.getByRole('button', { name: 'Schedule message' }).click();
  const localFuture = await page.evaluate(() => {
    const value = new Date(Date.now() + 2 * 60 * 60 * 1000);
    const pad = (part) => String(part).padStart(2, '0');
    return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}T${pad(value.getHours())}:${pad(value.getMinutes())}`;
  });
  await page.getByLabel('Send date and time').fill(localFuture);

  await Promise.all([
    page.waitForURL(/\/app\/scheduled\?.*scheduled=1/),
    page.locator('button[formaction^="/app/message/schedule"]').click(),
  ]);
  await expect(page.getByRole('status')).toHaveText('Message scheduled.');
  const scheduled = page.locator('.scheduled-item', { hasText: message });
  await expect(scheduled).toBeVisible();
  await expect(scheduled.getByRole('link', { name: '#general' })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.message-text', { hasText: message })).toHaveCount(0);
  await expect(composer).toHaveValue('');
  await page.getByRole('link', { name: 'Scheduled messages' }).click();
  await expect(scheduled).toBeVisible();
  await scheduled.getByRole('button', { name: /Cancel scheduled message/ }).click();
  await expect(page.getByRole('status')).toHaveText('Scheduled message cancelled.');
  await expect(page.locator('.scheduled-item', { hasText: message })).toHaveCount(0);
  await expect(page.getByText('You have no scheduled messages.')).toBeVisible();
});

test('[LATER-01 LATER-02 LATER-03 A11Y-01] Later saves privately and supports every current state', async ({ page, context, request }) => {
  await signIn(context);
  const text = `later browser qualification ${Date.now()}`;
  await postThroughTheAPI(request, text);
  await page.goto('/app');

  const message = page.locator('.message', { hasText: text });
  await message.focus();
  await page.keyboard.press('a');
  await expect(message.getByRole('button', { name: 'Remove from Later' })).toHaveAttribute('aria-pressed', 'true');
  await expect(message).toBeFocused();

  await page.getByRole('link', { name: 'Later' }).click();
  await expect(page.getByRole('heading', { name: 'Later', exact: true, level: 1 })).toBeVisible();
  await expect(page.getByRole('link', { name: 'In progress' })).toHaveAttribute('aria-current', 'page');
  let item = page.locator('.later-item', { hasText: text });
  await expect(item.getByRole('link', { name: '#general' })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);
  await item.getByRole('link', { name: '#general' }).click();
  await expect(page.locator('.message', { hasText: text })).toBeVisible();
  await page.getByRole('link', { name: 'Later' }).click();
  item = page.locator('.later-item', { hasText: text });

  await item.getByRole('button', { name: 'Mark complete' }).click();
  await expect(page.getByRole('status')).toHaveText('Saved item moved.');
  await expect(page.locator('.later-item', { hasText: text })).toHaveCount(0);
  await page.getByRole('link', { name: 'Completed' }).click();
  item = page.locator('.later-item', { hasText: text });
  await expect(item).toBeVisible();

  await item.getByRole('button', { name: 'Move to in progress' }).click();
  await expect(page.locator('.later-item', { hasText: text })).toHaveCount(0);
  await page.getByRole('link', { name: 'In progress' }).click();
  item = page.locator('.later-item', { hasText: text });
  await item.getByRole('button', { name: 'Archive' }).click();
  await page.getByRole('link', { name: 'Archived' }).click();
  item = page.locator('.later-item', { hasText: text });
  await expect(item).toBeVisible();

  await item.getByRole('button', { name: 'Remove from Later' }).click();
  await expect(page.getByRole('status')).toHaveText('Message removed from Later.');
  await expect(page.locator('.later-item', { hasText: text })).toHaveCount(0);
});

test('[REMIND-01 REMIND-02 REMIND-03 A11Y-01] reminders use the message shortcut, Later lifecycle, and built-in slash command', async ({ page, context, request }) => {
  await signIn(context);
  const sourceText = `reminder source ${Date.now()}`;
  await postThroughTheAPI(request, sourceText);
  await page.goto('/app');

  const source = page.locator('.message', { hasText: sourceText });
  await source.focus();
  await page.keyboard.press('m');
  const reminderMenu = source.locator('[data-reminder-menu]');
  await expect(reminderMenu).toHaveAttribute('open', '');
  await Promise.all([
    page.waitForURL(/\/app\/later\?.*changed=reminder/),
    reminderMenu.getByRole('button', { name: 'In 20 minutes' }).click(),
  ]);
  await expect(page.getByRole('status')).toHaveText('Reminder saved.');
  let reminder = page.locator('.later-item', { hasText: 'Message reminder' });
  await expect(reminder.getByRole('link', { name: 'View source message' })).toBeVisible();
  await expect(reminder.getByRole('button', { name: 'Mark complete' })).toBeVisible();

  await reminder.getByText('Edit', { exact: true }).click();
  const tomorrow = await page.evaluate(() => {
    const value = new Date(Date.now() + 24 * 60 * 60 * 1000);
    const pad = (part) => String(part).padStart(2, '0');
    return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`;
  });
  const description = `weekly reminder ${Date.now()}`;
  await reminder.getByLabel('Description').fill(description);
  await reminder.getByLabel('Date').fill(tomorrow);
  await reminder.getByLabel('Time').fill('12:30');
  await reminder.getByLabel('Repeat').selectOption('weekly');
  await reminder.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByRole('status')).toHaveText('Reminder saved.');
  reminder = page.locator('.later-item', { hasText: description });
  await expect(reminder).toContainText('Repeats weekly');
  await expectNoSeriousAccessibilityViolations(page);

  await reminder.getByRole('button', { name: 'Mark complete' }).click();
  await expect(page.getByRole('status')).toHaveText('Reminder completed.');
  await page.getByRole('link', { name: 'Completed' }).click();
  reminder = page.locator('.later-item', { hasText: description });
  await expect(reminder).toContainText('Completed');
  await reminder.getByRole('button', { name: 'Delete reminder' }).click();
  await expect(page.getByRole('status')).toHaveText('Reminder deleted.');
  await expect(page.locator('.later-item', { hasText: description })).toHaveCount(0);

  await page.getByRole('link', { name: 'Back to chat' }).click();
  const channelReminder = `channel reminder ${Date.now()}`;
  const composer = page.locator('form.composer textarea[name="text"]');
  await composer.fill(`/remind #general ${channelReminder} every Thursday at 9am`);
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page).toHaveURL(/\/app\/later\?.*filter=channel-reminders/);
  const channelItem = page.locator('.later-item', { hasText: channelReminder });
  await expect(channelItem.getByRole('link', { name: '#general' })).toBeVisible();
  await expect(channelItem.getByRole('button', { name: 'Delete reminder' })).toBeVisible();
  await expect(channelItem).toContainText('Repeats weekly');
  await expect(channelItem.getByRole('button', { name: 'Mark complete' })).toHaveCount(0);
  await expect(channelItem.getByText('Edit', { exact: true })).toHaveCount(0);

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.message-text', { hasText: channelReminder })).toHaveCount(0);
  await composer.fill('/remind list');
  await page.getByRole('button', { name: 'Send' }).click();
  await expect(page).toHaveURL(/\/app\/later\?.*filter=channel-reminders/);
  await expect(page.locator('.later-item', { hasText: channelReminder })).toBeVisible();
});

test('[A11Y-01 A11Y-02 A11Y-03] workspace and command discovery pass WCAG AA automation', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  await expectNoSeriousAccessibilityViolations(page);

  const { primary } = await slackModifiers(page);
  await page.locator('form.composer textarea[name="text"]').press(`${primary}+k`);
  await expect(page.getByRole('dialog', { name: 'Jump to a conversation' })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  await page.getByRole('button', { name: 'Close conversation switcher' }).click();
  await page.setViewportSize({ width: 320, height: 720 });
  await expectNoSeriousAccessibilityViolations(page);
});

test('[FILE-01 FILE-03 FILE-05] a file upload becomes a real message and an authenticated download', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  await page.getByText('Attach a file', { exact: true }).click();
  const title = `Browser file ${Date.now()}`;
  await page.getByLabel('Title (optional)').fill(title);
  await page.locator('#upload-file').setInputFiles({
    name: 'browser-report.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('browser file contents'),
  });
  await page.getByRole('button', { name: 'Upload and send' }).click();

  await expect(page).toHaveURL(/\/app\?channel=Cdev/);
  const card = page.locator('.message-file', { hasText: title }).last();
  await expect(card).toContainText('browser-report.txt');
  await expect(card).toContainText('text/plain');

  const [download] = await Promise.all([
    page.waitForEvent('download'),
    card.getByRole('link', { name: 'Download' }).click(),
  ]);
  expect(download.suggestedFilename()).toBe('browser-report.txt');
  const stream = await download.createReadStream();
  const chunks = [];
  for await (const chunk of stream) chunks.push(chunk);
  expect(Buffer.concat(chunks).toString()).toBe('browser file contents');
});

test('[APP-01 APP-02 APP-09] developer app console creates, validates, edits, and deletes a real app', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  await page.getByRole('link', { name: 'Developer apps', exact: true }).click();
  await expect(page).toHaveURL(/\/app\/developer\/apps$/);
  await expect(page.getByRole('heading', { name: 'Developer apps' }).last()).toBeVisible();

  const manifest = page.getByRole('textbox', { name: 'App manifest (JSON)' });
  await manifest.fill('{"display_information":{}}');
  await page.getByRole('button', { name: 'Create app' }).click();
  await expect(page.getByRole('alert')).toContainText('Fix the manifest errors');
  await expect(page.getByLabel('Manifest errors')).toContainText('/display_information/name');

  const name = `Browser app ${Date.now()}`;
  const validManifest = JSON.stringify({
    display_information: { name, description: 'Created through the browser console' },
    oauth_config: {
      redirect_urls: ['https://client.example/oauth'],
      scopes: { bot: ['chat:write'], user: ['users:read'] },
    },
    settings: {
      token_rotation_enabled: true,
      socket_mode_enabled: true,
      event_subscriptions: { bot_events: ['message.channels'] },
    },
  }, null, 2);
  await manifest.fill(validManifest);
  await page.getByRole('button', { name: 'Create app' }).click();
  await expect(page.getByRole('heading', { name: 'Save these app credentials now' })).toBeVisible();
  await expect(page.locator('.secret code')).toHaveCount(4);
  await expect(page.getByRole('link', { name: 'Open install flow' })).toHaveAttribute('href', /scope=chat%3Awrite/);

  // The one-time POST response must replace its history entry with the safe
  // GET URL before refresh. Waiting for the secret alone can observe the DOM
  // before the trailing history script has run, which made WebKit reload the
  // POST-only /create route.
  await expect(page).toHaveURL(/\/app\/developer\/apps\?app=/);
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Save these app credentials now' })).toHaveCount(0);
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible();
  await manifest.fill(validManifest.replace(name, `${name} edited`));
  await page.getByRole('button', { name: 'Save manifest' }).click();
  await expect(page.getByRole('status')).toContainText('Manifest saved');
  await expect(page.getByText('Manifest v2', { exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Generate app-level token' }).click();
  await expect(page.getByRole('heading', { name: 'Save this app-level token now' })).toBeVisible();
  await expect(page.locator('.secret code').first()).toContainText('xapp-');
  await expect(page.locator('.secret')).toContainText('connections:write');
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Save this app-level token now' })).toHaveCount(0);

  await page.getByRole('button', { name: 'Issue configuration token' }).click();
  await expect(page.getByRole('heading', { name: 'Save this configuration token now' })).toBeVisible();
  await expect(page.locator('.secret code').first()).toContainText('xoxe.xoxp-');

  await page.getByRole('button', { name: 'Delete app' }).click();
  await expect(page).toHaveURL(/\/app\/developer\/apps$/);
  await expect(page.getByText('You have not created an app yet.')).toBeVisible();
});

test('[APP-03 APP-07 MSG-01] JSON-authored blocks, attachments, and unfurls render as usable messages', async ({ page, context, request }) => {
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
      { type: 'markdown', text: `## Release ${stamp}\nStreaming-ready output` },
      {
        type: 'table',
        rows: [
          [{ type: 'raw_text', text: 'Service' }, { type: 'raw_text', text: 'Latency' }],
          [{ type: 'raw_text', text: 'API' }, { type: 'raw_number', value: 42 }],
        ],
      },
      {
        type: 'actions',
        elements: [{ type: 'button', action_id: 'view_build', text: { type: 'plain_text', text: 'View build' } }],
      },
    ],
  });
  const currentBlockTitle = `Current Block Kit ${stamp}`;
  await postPayloadThroughTheAPI(request, {
    channel: CHANNEL,
    blocks: [
      { type: 'alert', level: 'success', text: { type: 'mrkdwn', text: '*Validated* against the current catalog' } },
      {
        type: 'card',
        title: { type: 'plain_text', text: currentBlockTitle },
        subtitle: { type: 'plain_text', text: 'Production' },
        body: { type: 'mrkdwn', text: '*Healthy* and serving traffic' },
      },
      {
        type: 'carousel',
        elements: [
          { type: 'card', title: { type: 'plain_text', text: `Region EU ${stamp}` } },
          { type: 'card', title: { type: 'plain_text', text: `Region US ${stamp}` } },
        ],
      },
      {
        type: 'task_card',
        task_id: 'browser-task',
        title: `Qualify task ${stamp}`,
        status: 'complete',
        output: {
          type: 'rich_text',
          elements: [{ type: 'rich_text_section', elements: [{ type: 'text', text: 'Browser verified', style: { bold: true } }] }],
        },
      },
      {
        type: 'plan',
        title: `Qualification plan ${stamp}`,
        tasks: [{ task_id: 'browser-plan-task', title: 'Render current blocks', status: 'complete' }],
      },
      {
        type: 'data_table',
        caption: `Health data ${stamp}`,
        rows: [
          [{ type: 'raw_text', text: 'Service' }, { type: 'raw_text', text: 'Latency' }],
          [{ type: 'raw_text', text: 'API' }, { type: 'raw_number', value: 42 }],
        ],
      },
      {
        type: 'container',
        width: 'wide',
        title: { type: 'plain_text', text: `Bulk update ${stamp}` },
        subtitle: { type: 'mrkdwn', text: 'Review *two* records' },
        is_collapsible: true,
        default_collapsed: true,
        child_blocks: [
          { type: 'section', text: { type: 'mrkdwn', text: '*DCW-1024* → Closed' } },
          { type: 'divider' },
          { type: 'context', elements: [{ type: 'plain_text', text: 'Ready to apply' }] },
        ],
      },
      {
        type: 'data_visualization',
        title: `Weekly health ${stamp}`,
        chart: {
          type: 'line',
          series: [{ name: 'Availability', data: [{ label: 'Week 1', value: 99.9 }, { label: 'Week 2', value: 99.99 }] }],
          axis_config: { categories: ['Week 1', 'Week 2'], x_label: 'Week', y_label: 'Percent' },
        },
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
  await expect(blockMessage.locator('strong', { hasText: 'Production' })).toBeVisible();
  await expect(blockMessage.getByText('Production is healthy', { exact: true })).toBeVisible();
  await expect(blockMessage.getByText('Region: eu-west', { exact: true })).toBeVisible();
  await expect(blockMessage.getByRole('heading', { level: 2, name: `Release ${stamp}` })).toBeVisible();
  await expect(blockMessage.getByText('Streaming-ready output', { exact: true })).toBeVisible();
  await expect(blockMessage.locator('.block-table')).toContainText('Service');
  await expect(blockMessage.locator('.block-table')).toContainText('42');
  // A user-authored message has no owning app endpoint. Rendering its button
  // as an inert label would claim an action exists when no Slack app can
  // receive it, so the first-party client withholds that control.
  await expect(blockMessage.getByText('View build', { exact: true })).toHaveCount(0);
  await expect(blockMessage.locator('.message-text')).toHaveCount(0);
  await expect(blockMessage.getByText(`notification fallback ${stamp}`, { exact: true })).toHaveCount(0);
  await expect(blockMessage.getByText('Edit', { exact: true })).toHaveCount(0);
  await blockMessage.hover();
  await expect(blockMessage.getByText('Delete', { exact: true })).toBeVisible();

  const currentBlockMessage = page.locator('.message', { hasText: currentBlockTitle });
  await expect(currentBlockMessage.locator('.message-block.alert.success')).toContainText('Validated against the current catalog');
  await expect(currentBlockMessage.locator('.message-block.card')).toContainText(currentBlockTitle);
  await expect(currentBlockMessage.locator('.block-card-body strong')).toHaveText('Healthy');
  await expect(currentBlockMessage.locator('.block-carousel-card')).toHaveCount(2);
  await expect(currentBlockMessage.locator('.block-carousel-card').first()).toContainText(`Region EU ${stamp}`);
  await expect(currentBlockMessage.locator('.message-block.task-card')).toContainText(`Qualify task ${stamp}`);
  await expect(currentBlockMessage.getByText('Browser verified', { exact: true })).toBeVisible();
  await expect(currentBlockMessage.locator('.message-block.plan')).toContainText(`Qualification plan ${stamp}`);
  await expect(currentBlockMessage.locator('.message-block.data-table caption')).toHaveText(`Health data ${stamp}`);
  await expect(currentBlockMessage.locator('.message-block.data-table th').first()).toHaveAttribute('scope', 'col');
  const container = currentBlockMessage.locator('.message-block.container details');
  await expect(container.locator('summary')).toContainText(`Bulk update ${stamp}`);
  await expect(container).not.toHaveAttribute('open', '');
  await container.locator('summary').click();
  await expect(container).toHaveAttribute('open', '');
  await expect(container).toContainText('DCW-1024');
  await expect(currentBlockMessage.locator('.message-block.data-visualization figcaption')).toHaveText(`Weekly health ${stamp}`);
  await expect(currentBlockMessage.locator('.message-block.data-visualization polyline')).toHaveCount(1);
  await expect(currentBlockMessage.locator('.block-chart-data th', { hasText: 'Availability' })).toHaveAttribute('scope', 'col');

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
test('[THREAD-01 THREAD-02] opening a thread renders the thread and its composer', async ({ page, context, request }) => {
  await signIn(context);
  const rootText = `thread root ${Date.now()}`;
  const root = await postThroughTheAPI(request, rootText);

  await page.goto('/app');
  const rootMessage = page.locator('.message', { hasText: rootText });
  await rootMessage.hover();
  const reply = rootMessage.getByRole('link', { name: 'Reply in thread' });
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
test('[COMP-01 NAV-02 NAV-03 APP-05] the composer and workspace honour Slack web keyboard contracts', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  const { primary, activity } = await slackModifiers(page);
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

  // APP-05: slash opens installed/built-in command discovery in the composer;
  // choosing /shrug invokes the built-in instead of posting the raw command.
  await composer.fill('/shr');
  const commandList = page.getByRole('listbox', { name: 'Shortcuts and slash commands' });
  await expect(commandList).toBeVisible();
  await expect(commandList.getByRole('option', { name: /\/shrug/ })).toBeVisible();
  await composer.press('Enter');
  await expect(composer).toHaveValue('/shrug ');
  await composer.pressSequentially('release day');
  await composer.press('Enter');
  await expect(page.locator('.message-text').last()).toContainText('release day ¯\\_(ツ)_/¯');

  // NAV-02: Slack web's global search shortcut is Control/Command+G, and
  // Escape returns to composing without submitting or losing text.
  await composer.fill('draft survives search');
  await composer.press(`${primary}+g`);
  const search = page.locator('#workspace-search');
  await expect(search).toBeFocused();
  await search.press('Escape');
  await expect(composer).toBeFocused();
  await expect(composer).toHaveValue('draft survives search');

  // NAV-03: Control/Command+K is the conversation switcher, not search.
  await composer.press(`${primary}+k`);
  const switcher = page.getByRole('dialog', { name: 'Jump to a conversation' });
  await expect(switcher).toBeVisible();
  await expect(switcher.getByPlaceholder('Jump to a conversation')).toBeFocused();
  await switcher.getByRole('button', { name: 'Close conversation switcher' }).click();
  await expect(composer).toBeFocused();
  await expect(composer).toHaveValue('draft survives search');

  // Slack's dedicated Activity chord is desktop-only. Slack web uses the
  // navigation-tab shortcut (Activity is the default third tab).
  await page.keyboard.press(activity);
  await expect(page).toHaveURL(/\/app\/activity/);
  await expect(page.getByRole('heading', { name: 'Activity', exact: true, level: 2 })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Activity filters' })).toBeVisible();
});

test('[COMP-02 COMP-03 DRAFT-01 FILE-01] composer formatting, mentions, emoji, drafts, and file preview are functional', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  const { primary } = await slackModifiers(page);
  await composer.fill('format me');
  await composer.evaluate((field) => field.setSelectionRange(0, field.value.length));
  await composer.press(`${primary}+b`);
  await expect(composer).toHaveValue('*format me*');

  await composer.evaluate((field) => field.setSelectionRange(field.value.length, field.value.length));
  await page.getByRole('button', { name: 'Choose an emoji' }).click();
  await page.getByRole('menuitem', { name: 'Party popper' }).click();
  await expect(composer).toHaveValue('*format me*:tada:');
  await composer.press('Enter');
  const formatted = page.locator('.message').last();
  await expect(formatted.locator('strong')).toHaveText('format me');
  await expect(formatted.locator('.message-text')).toContainText(':tada:');

  await composer.fill('@');
  const suggestions = page.getByRole('listbox', { name: 'Mention suggestions' });
  await expect(suggestions).toBeVisible();
  await composer.press('Enter');
  await expect(composer).toHaveValue(/^<@U[^>]+> $/);

  const draft = `durable draft ${Date.now()}`;
  await composer.fill(draft);
  await page.reload();
  await expect(composer).toHaveValue(draft);

  await page.getByText('Attach a file', { exact: true }).click();
  await page.locator('#upload-file').setInputFiles({
    name: 'preview.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('preview body'),
  });
  await expect(page.locator('#upload-preview')).toContainText('preview.txt');
  await expect(page.locator('#upload-preview')).toContainText('12 B');

  const sent = `draft cleared ${Date.now()}`;
  await composer.fill(sent);
  await composer.press('Enter');
  await expect(page.locator('.message-text').last()).toHaveText(sent);
  await expect(composer).toHaveValue('');
  await page.reload();
  await expect(composer).toHaveValue('');
});

test('[MSG-01 MSG-02 MSG-03 MSG-04 ACT-01 ACT-02] message reading and actions honour Slack keyboard navigation', async ({ page, context, request }) => {
  await signIn(context);
  const firstText = `keyboard first ${Date.now()}`;
  const secondText = `keyboard second ${Date.now()}`;
  const lastText = `keyboard last ${Date.now()}`;
  const first = await postThroughTheAPI(request, firstText);
  const second = await postThroughTheAPI(request, secondText);
  const last = await postThroughTheAPI(request, lastText);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  await composer.fill('');
  await composer.press('ArrowUp');
  const lastMessage = page.locator('.message').filter({ has: page.locator('.message-text', { hasText: lastText }) });
  const secondMessage = page.locator('.message').filter({ has: page.locator('.message-text', { hasText: secondText }) });
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
  await page.waitForLoadState('domcontentloaded');

  const returned = page.locator('.message').filter({ has: page.locator('.message-text', { hasText: lastText }) });
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
test('[CONV-01 COMP-01] a public-channel preview can be joined and posted to', async ({ page, context, request }) => {
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

test('[CONV-03 CONV-04] conversation details manage a channel without falling back to the API', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const stamp = Date.now();
  const originalName = `journey-${stamp}`;
  await page.getByText('Add channel', { exact: false }).click();
  await page.getByLabel('Channel name').fill(originalName);
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page.locator('.channel-title')).toHaveText(`# ${originalName}`);

  await page.getByRole('link', { name: 'Open conversation details' }).click();
  const details = page.getByRole('dialog', { name: `# ${originalName}` });
  await expect(details).toBeVisible();
  await expect(details).toContainText('Every available workspace member is already in this channel.');

  const renamed = `release-${stamp}`;
  await details.getByLabel('Channel name').fill(renamed);
  await details.getByRole('button', { name: 'Rename' }).click();
  await expect(page.getByRole('dialog', { name: `# ${renamed}` })).toBeVisible();

  await page.getByLabel('Topic').fill('Shipping this week');
  await page.getByRole('button', { name: 'Save topic' }).click();
  await expect(page.getByText('Shipping this week', { exact: true }).first()).toBeVisible();

  await page.getByLabel('Purpose').fill('Coordinate the release');
  await page.getByRole('button', { name: 'Save purpose' }).click();
  await expect(page.getByText('Coordinate the release', { exact: true }).first()).toBeVisible();

  await page.getByRole('button', { name: 'Archive channel' }).click();
  await expect(page.getByRole('dialog')).toContainText('Archived');
  await expect(page.locator('form.composer')).toHaveCount(0);
  await page.getByRole('button', { name: 'Unarchive channel' }).click();
  await expect(page.locator('form.composer')).toBeVisible();

  await page.getByRole('button', { name: 'Leave channel' }).click();
  await expect(page.getByText('Not joined', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Join channel' })).toBeVisible();
});

// The page closed its EventSource on the first submit and never reopened it, and
// separately suppressed every event while the autofocused composer held focus —
// so in the default state live delivery never reached the timeline at all.
test('[RESILIENCE-01] live delivery keeps reaching the timeline after posting', async ({ page, context, request }) => {
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
test('[ACT-02 ACT-03] reactions and pins render and reverse in place', async ({ page, context, request }) => {
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

test('[MSG-03 MSG-04] a member can edit and delete their own message in place', async ({ page, context }) => {
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
  await target.getByRole('button', { name: 'Save changes', exact: true }).click();
  await expect(page.locator('.message', { hasText: changed })).toHaveCount(1);
  await expect(page.locator('.message', { hasText: original })).toHaveCount(0);

  const changedTarget = page.locator('.message', { hasText: changed });
  await changedTarget.hover();
  await changedTarget.getByText('Delete', { exact: true }).click();
  await changedTarget.getByRole('button', { name: 'Delete this message' }).click();
  await expect(page.locator('.message', { hasText: changed })).toHaveCount(0);
});

test('[CONV-02 NAV-04] channel creation is reachable and conversation shortcuts switch channels', async ({ page, context }) => {
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

test('[PROFILE-02 STATUS-01] profile editing presents one human-facing photo field and saves status', async ({ page, context }) => {
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

test('[NAV-06] theme choice persists across workspace pages', async ({ page, context }) => {
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
test('[COMP-01 RESILIENCE-04] a rejected post explains itself and keeps the draft', async ({ page, context }) => {
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
  await target.hover();
  await target.getByRole('button', { name: 'Pin' }).click();
  const actionError = page.locator('#action-feedback');
  await expect(actionError).toBeVisible();
  await expect(actionError).not.toContainText('message was kept');
  await expect(error).toHaveText(sendError);
  await expect(composer).toHaveValue(doomed);
});

// The old 64px rail gave every channel the same # glyph and every DM the same @
// glyph. Accessible names did not help a sighted mobile reader choose one.
test('[RESPONSIVE-01 A11Y-01 THREAD-01] the narrow layout exposes named navigation and keeps the thread reachable', async ({ page, context, request }) => {
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

test('[RESPONSIVE-01] named mobile navigation survives without JavaScript', async ({ browser, baseURL }) => {
  const context = await browser.newContext({
    baseURL,
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
test('[RESILIENCE-03] the workspace entry point renders without post-processing its own markup', async ({ page, context }) => {
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
test('[AUTH-01] the workspace is protected and its own scripts run under its policy', async ({ page, context }) => {
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
test('[RESILIENCE-02 COMP-01] a second submit while the first is in flight posts one message', async ({ page, context }) => {
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
test('[MSG-01 RESILIENCE-01] a message sent while reading older history is not lost', async ({ page, context, request }) => {
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
test('[AUTH-03] signing out ends the session and the signed-out page is terminal', async ({ page, context }) => {
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
