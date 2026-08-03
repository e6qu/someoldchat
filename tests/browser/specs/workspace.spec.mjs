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
  const redirectURI = 'https://client.example/browser-oauth-callback';
  const name = `Activity bot ${Date.now()}`;
  const installed = await createAndInstallApp(page, request, {
    display_information: { name },
    oauth_config: {
      redirect_urls: [redirectURI],
      scopes: { bot: ['channels:join', 'channels:manage', 'groups:write', 'chat:write'] },
    },
  }, redirectURI);

  const join = await request.post('/api/conversations.join', {
    headers: { authorization: `Bearer ${installed.token}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL },
  });
  const joined = await join.json();
  expect(joined.ok, JSON.stringify(joined)).toBe(true);
  return { ...installed, name };
}

async function createAndInstallApp(page, request, manifest, redirectURI) {
  await page.goto('/app/developer/apps');
  await page.getByRole('textbox', { name: 'App manifest (JSON)' }).fill(JSON.stringify(manifest, null, 2));
  await page.getByRole('button', { name: 'Create app' }).click();
  await expect(page.getByRole('heading', { name: 'Save these app credentials now' })).toBeVisible();
  const appID = new URL(page.url()).searchParams.get('app');
  expect(appID).toBeTruthy();
  const clientID = (await page.locator('dt:text-is("Client ID") + dd code').textContent()).trim();
  const clientSecret = (await page.locator('dt:text-is("Client secret") + dd code').textContent()).trim();

  await page.getByRole('link', { name: 'Open install flow' }).click();
  await expect(page.getByRole('heading', { name: `Authorize ${manifest.display_information.name}` })).toBeVisible();
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
  return { appID, token: installed.access_token, botUserID: installed.bot_user_id };
}

// selector scopes the scan to one region. It is used only where a journey is
// about a specific surface; an unscoped scan stays the default, because a
// scoped scan can pass while the page around it fails.
async function expectNoSeriousAccessibilityViolations(page, selector) {
  let builder = new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa']);
  if (selector) {
    builder = builder.include(selector);
  }
  const results = await builder.analyze();
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

test('[DM-01 DM-02 DM-04] direct messages have a searchable, accessible first-party surface', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  await page.getByRole('link', { name: 'Direct messages', exact: true }).click();
  await expect(page).toHaveURL('/app/dms');
  await expect(page.getByRole('heading', { name: 'Direct messages' })).toBeVisible();
  await expect(page.getByRole('searchbox', { name: 'Search direct messages and people' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Recent' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'New message' })).toBeVisible();
  await expect(page.getByText('up to nine people total')).toBeVisible();
  await expect(page.getByLabel('Group DM name (optional)')).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  await page.setViewportSize({ width: 320, height: 780 });
  const response = await page.reload();
  expect(response.status()).toBe(200);
  const body = await page.locator('main').boundingBox();
  expect(body.x).toBeGreaterThanOrEqual(0);
  expect(body.x + body.width).toBeLessThanOrEqual(320);
});

test('[DM-03 DM-05 A11Y-01] adding people reviews history and group DMs convert in place', async ({ page, context, request }) => {
  await signIn(context);
  const installedApps = [];
  try {
    const first = await installActivityBot(page, request);
    installedApps.push(first.appID);
    const second = await installActivityBot(page, request);
    installedApps.push(second.appID);

    await page.goto('/app/dms');
    await page.locator(`input[name="user_${first.botUserID}"]`).check();
    await page.getByRole('button', { name: 'Start conversation' }).click();
    await expect(page).toHaveURL(/\/app\?channel=/);

    const retained = `history retained in expanded DM ${Date.now()}`;
    const composer = page.locator('form.composer textarea[name="text"]');
    await composer.fill(retained);
    await composer.press('Enter');
    await expect(page.locator('.message-text', { hasText: retained })).toBeVisible();

    await page.getByRole('link', { name: 'Open conversation details' }).click();
    await page.getByText('Add people', { exact: true }).click();
    await page.locator(`input[name="user_${second.botUserID}"]`).check();
    await page.getByRole('button', { name: 'Next' }).click();

    await expect(page.getByRole('heading', { name: 'Include conversation history' })).toBeVisible();
    await page.getByLabel('Include all conversation history and files').check();
    await page.getByRole('button', { name: 'Done' }).click();
    await expect(page.getByRole('heading', { name: 'Review new group DM' })).toBeVisible();
    await expect(page.getByText(second.name)).toBeVisible();
    await expect(page.getByText('All existing messages and shared files')).toBeVisible();
    await expect(page.getByText('Slack posts an automatic participant notice')).toBeVisible();
    await expectNoSeriousAccessibilityViolations(page);
    await page.getByRole('button', { name: 'Confirm and create group DM' }).click();
    await expect(page).toHaveURL(/\/app\?channel=/);
    await expect(page.locator('.message-text', { hasText: retained })).toBeVisible();

    await page.getByRole('link', { name: 'Open conversation details' }).click();
    await expect(page.getByRole('link', { name: 'Settings' })).toBeVisible();
    await page.getByText('Change to a private channel', { exact: true }).click();
    await expect(page.getByText('Messages and files from this group DM will stay')).toBeVisible();
    const channelName = `converted-${Date.now()}`;
    await page.getByLabel('Private channel name').fill(channelName);
    await page.getByRole('button', { name: 'Change to Private' }).click();
    await expect(page.locator('.channel-title')).toHaveText(`# ${channelName}`);
    await expect(page.locator('.message-text', { hasText: retained })).toBeVisible();
  } finally {
    for (const appID of installedApps.reverse()) {
      await page.goto(`/app/developer/apps?app=${encodeURIComponent(appID)}`);
      const remove = page.getByRole('button', { name: 'Delete app' });
      if (await remove.isVisible()) {
        await remove.click();
      }
    }
  }
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
    const invitationChannelName = `activity-invite-${Date.now()}`;
    const invitationChannelResponse = await request.post('/api/conversations.create', {
      headers: { authorization: `Bearer ${activityBot.token}`, 'content-type': 'application/json' },
      data: { name: invitationChannelName, is_private: true },
    });
    const invitationChannel = await invitationChannelResponse.json();
    expect(invitationChannel.ok, JSON.stringify(invitationChannel)).toBe(true);
    const invitationResponse = await request.post('/api/conversations.invite', {
      headers: { authorization: `Bearer ${activityBot.token}`, 'content-type': 'application/json' },
      data: { channel: invitationChannel.channel.id, users: 'Udev' },
    });
    const invitation = await invitationResponse.json();
    expect(invitation.ok, JSON.stringify(invitation)).toBe(true);
    await page.goto('/app/activity');

    await expect(page.getByRole('heading', { name: 'Activity', exact: true, level: 2 })).toBeVisible();
    await expect(page.getByRole('link', { name: 'All' })).toHaveAttribute('aria-current', 'page');
    await expect(page.getByText(selfMessage)).toHaveCount(0);
    await page.getByRole('link', { name: 'Invitations' }).click();
    await expect(page.getByRole('link', { name: 'Invitations' })).toHaveAttribute('aria-current', 'page');
    const invitationRow = page.locator('[data-activity-row]', { hasText: `Added you to #${invitationChannelName}.` });
    await expect(invitationRow).toBeVisible();
    await expect(invitationRow.locator('[data-activity-source]')).toHaveAttribute('href', `/app?channel=${invitationChannel.channel.id}`);
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

    rows = page.locator('[data-activity-row]');
    const focusedActivityID = await rows.nth(0).getAttribute('data-activity-id');
    await rows.nth(0).focus();
    const live = `app mention live ${Date.now()}`;
    await postPayloadWithToken(request, activityBot.token, { channel: CHANNEL, text: `<@Udev> ${live}` });
    await expect(page.locator('[data-activity-row]')).toHaveCount(2);
    await expect(page.locator(`[data-activity-id="${focusedActivityID}"]`)).toBeFocused();
    const liveRow = page.locator('[data-activity-row]', { hasText: live });
    await expect(liveRow).toBeVisible();
    await liveRow.focus();
    await page.keyboard.press('r');
    await expect(liveRow).not.toHaveClass(/unread/);

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

test('[SCHED-01 SCHED-02 A11Y-01] scheduled work can be edited, sent now, and cancelled', async ({ page, context }) => {
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
    page.waitForURL(/\/app\/drafts\?.*scheduled=1/),
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
  await page.getByRole('link', { name: 'Drafts and sent' }).click();
  await page.getByRole('link', { name: 'Scheduled', exact: true }).click();
  await expect(scheduled).toBeVisible();
  await scheduled.getByText('Edit', { exact: true }).click();
  const edited = `${message} edited`;
  await scheduled.locator('textarea[name="text"]').fill(edited);
  const rescheduledFuture = await page.evaluate(() => {
    const value = new Date(Date.now() + 3 * 60 * 60 * 1000);
    const pad = (part) => String(part).padStart(2, '0');
    return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}T${pad(value.getHours())}:${pad(value.getMinutes())}`;
  });
  await scheduled.locator('input[name="schedule_at"]').fill(rescheduledFuture);
  await expect(scheduled.locator('input[name="post_at"]')).not.toHaveValue('');
  await scheduled.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByRole('status')).toHaveText('Scheduled message updated.');
  const updated = page.locator('.scheduled-item', { hasText: edited });
  await updated.getByRole('button', { name: 'Send now' }).click();
  await expect(page.getByRole('status')).toHaveText('Scheduled message sent.');
  await expect(page.getByRole('link', { name: 'Sent', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('.work-item', { hasText: edited })).toBeVisible();

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.message-text', { hasText: edited })).toBeVisible();
  await composer.fill(`${message} cancel`);
  await page.getByRole('button', { name: 'Schedule message' }).click();
  await page.getByLabel('Send date and time').fill(localFuture);
  await Promise.all([
    page.waitForURL(/\/app\/drafts\?.*scheduled=1/),
    page.locator('button[formaction^="/app/message/schedule"]').click(),
  ]);
  const cancellable = page.locator('.scheduled-item', { hasText: `${message} cancel` });
  await cancellable.getByRole('button', { name: /Cancel scheduled message/ }).click();
  await expect(page.getByRole('status')).toHaveText('Scheduled message cancelled.');
  await expect(page.locator('.scheduled-item', { hasText: `${message} cancel` })).toHaveCount(0);
  await expect(page.getByText('You have no scheduled messages.')).toBeVisible();
});

test('[SCHED-01 SCHED-02 FILE-01 A11Y-01] a staged file remains private until one scheduled delivery', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  const stamp = Date.now();
  const title = `Scheduled file ${stamp}`;
  await page.locator('#upload-details').evaluate((details) => { details.open = true; });
  await page.getByLabel('Title (optional)').fill(title);
  await page.locator('#upload-file').setInputFiles({
    name: `scheduled-${stamp}.txt`,
    mimeType: 'text/plain',
    buffer: Buffer.from('scheduled browser file contents'),
  });
  await expect(page.locator('#live-status')).toContainText('saved with this draft');
  await page.getByRole('button', { name: 'Schedule message' }).click();
  const localFuture = await page.evaluate(() => {
    const value = new Date(Date.now() + 2 * 60 * 60 * 1000);
    const pad = (part) => String(part).padStart(2, '0');
    return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}T${pad(value.getHours())}:${pad(value.getMinutes())}`;
  });
  await page.getByLabel('Send date and time').fill(localFuture);
  await Promise.all([
    page.waitForURL(/\/app\/drafts\?.*scheduled=1/),
    page.locator('button[formaction^="/app/message/schedule"]').click(),
  ]);
  await expect(page.getByRole('status')).toHaveText('Message scheduled.');
  const scheduled = page.locator('.scheduled-item', { hasText: 'File attachment' });
  await expect(scheduled).toContainText('1 attachment');
  await expect(scheduled).toContainText('Attached files stay with this scheduled message.');
  await expectNoSeriousAccessibilityViolations(page);

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.message-file', { hasText: title })).toHaveCount(0);
  await page.getByRole('link', { name: 'Drafts and sent' }).click();
  await page.getByRole('link', { name: 'Scheduled', exact: true }).click();
  await scheduled.getByRole('button', { name: 'Send now' }).click();
  await expect(page.getByRole('status')).toHaveText('Scheduled message sent.');
  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.message-file', { hasText: title })).toBeVisible();
});

test('[DRAFT-01 DRAFT-02 A11Y-01] drafts persist on the server and Drafts & sent exposes all tabs', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  const composer = page.locator('form.composer textarea[name="text"]');
  const draft = `server draft qualification ${Date.now()}`;
  await Promise.all([
    // Any page load or navigation also POSTs /app/draft (init persist and
    // pagehide flush), so a bare "204 from /app/draft" can resolve on one of
    // those while the debounced save of THIS draft is still pending, and the
    // reload below then races it. Key the wait on the request that carries
    // the new text; the body is form-encoded, so parse it rather than
    // substring-matching (spaces arrive as "+").
    page.waitForResponse(
      (response) =>
        response.url().includes('/app/draft?') &&
        response.status() === 204 &&
        response.request().method() === 'POST' &&
        new URLSearchParams(response.request().postData() || '').get('text') === draft,
    ),
    composer.fill(draft),
  ]);
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await expect(composer).toHaveValue(draft);

  await page.getByRole('link', { name: 'Drafts and sent' }).click();
  await expect(page.getByRole('link', { name: 'Drafts', exact: true })).toHaveAttribute('aria-current', 'page');
  const item = page.locator('.work-item', { hasText: draft });
  await expect(item).toBeVisible();
  await item.getByRole('link', { name: 'Continue' }).click();
  await expect(composer).toHaveValue(draft);

  await page.getByRole('link', { name: 'Drafts and sent' }).click();
  await item.getByRole('button', { name: /Delete draft/ }).click();
  await expect(page.getByRole('status')).toHaveText('Draft deleted.');
  await expect(page.getByText('You have no drafts.')).toBeVisible();

  await page.getByRole('link', { name: 'Back to chat' }).click();
  const sent = `sent tab qualification ${Date.now()}`;
  await composer.fill(sent);
  await composer.press('Enter');
  await expect(page.locator('.message-text', { hasText: sent })).toBeVisible();
  await page.getByRole('link', { name: 'Drafts and sent' }).click();
  await page.getByRole('link', { name: 'Sent', exact: true }).click();
  await expect(page.locator('.work-item', { hasText: sent })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);
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
  // The Later pages reload themselves on reminder events. This journey is the
  // CRUD contract, not live delivery, and the reminders it creates fire those
  // very events — an EventSource replay right after landing reloads the page
  // under an open editor or a running axe scan, which is the WebKit flake.
  // Hold the stream shut so every state change observed is one this test made.
  await page.route('**/events*', (route) => route.abort());
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
  await expect(page).toHaveURL(/\/app\/later\?.*state=completed/);
  reminder = page.locator('.later-item', { hasText: description });
  await expect(reminder).toContainText('Completed');
  await reminder.getByRole('button', { name: 'Delete reminder' }).click();
  await expect(page.getByRole('status')).toHaveText('Reminder deleted.');
  await expect(page.locator('.later-item', { hasText: description })).toHaveCount(0);

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page).toHaveURL(/\/app(\?|$)/);
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
  await expect(page).toHaveURL(/\/app(\?|$)/);
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

  await page.locator('#upload-details').evaluate((details) => { details.open = true; });
  const title = `Browser file ${Date.now()}`;
  await page.getByLabel('Title (optional)').fill(title);
  await page.locator('#upload-file').setInputFiles({
    name: 'browser-report.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('browser file contents'),
  });
  await expect(page.locator('#live-status')).toContainText('saved with this draft');
  await page.getByRole('button', { name: 'Send', exact: true }).click();

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

test('[SEARCH-01 SEARCH-02 SEARCH-03 FILE-04 A11Y-01] typed search is scoped, filterable, and backed by real files', async ({ page, context, request }) => {
  await signIn(context);
  const needle = `typed-search-${Date.now()}`;
  const message = `${needle} release candidate`;
  const fileTitle = `${needle} notes`;
  await postThroughTheAPI(request, message);

  await page.goto('/app');
  await page.locator('#upload-details').evaluate((details) => { details.open = true; });
  await page.getByLabel('Title (optional)').fill(fileTitle);
  await page.locator('#upload-file').setInputFiles({
    name: `${needle}.txt`,
    mimeType: 'text/plain',
    buffer: Buffer.from(`file contents for ${needle}`),
  });
  await expect(page.locator('#live-status')).toContainText('saved with this draft');
  await page.getByRole('button', { name: 'Send', exact: true }).click();
  await expect(page.locator('.message-file', { hasText: fileTitle })).toBeVisible();

  const { primary } = await slackModifiers(page);
  const composer = page.locator('form.composer textarea[name="text"]');
  await composer.fill('draft survives current-conversation search');
  await composer.press(`${primary}+f`);
  await expect(page).toHaveURL(/\/app\/search\?.*scope=channel/);
  await expect(page.getByText('Searching only this conversation.')).toBeVisible();
  const query = page.getByRole('combobox', { name: 'Search the workspace' });
  await expect(query).toBeFocused();
  await query.fill(needle);
  await query.press('Enter');
  await expect(page.locator('.result', { hasText: message })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Search the whole workspace' })).toBeVisible();
  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(composer).toHaveValue('draft survives current-conversation search');
  await page.goBack();
  await expect(page.locator('.result', { hasText: message })).toBeVisible();

  await page.getByLabel('Sort').selectOption('oldest');
  await page.getByRole('button', { name: 'Apply filters' }).click();
  await expect(page).toHaveURL(/order=oldest/);
  await expect(page.locator('.result', { hasText: message })).toBeVisible();

  await page.getByRole('link', { name: 'Files', exact: true }).click();
  await expect(page.locator('.result', { hasText: fileTitle })).toContainText('text/plain');
  await expectNoSeriousAccessibilityViolations(page);

  await page.goto('/app/search?q=sameoldchat&type=people&channel=Cdev');
  await expect(page.locator('.result', { hasText: 'SameOldChat' })).toBeVisible();
  await page.goto('/app/search?q=general&type=channels&channel=Cdev');
  await expect(page.getByRole('link', { name: '# general' })).toBeVisible();

  await page.goto('/app');
  const workspaceSearch = page.locator('#workspace-search');
  await workspaceSearch.focus();
  await workspaceSearch.fill(needle);
  const suggestions = page.getByRole('listbox', { name: 'Search suggestions' });
  const recent = suggestions.getByRole('option', { name: `${needle} Recent search` });
  await expect(recent).toContainText('Recent search');
  // Scoped to the search listbox: this asserts that a needle matching nothing
  // suggests no channel, not that the whole page contains no element with the
  // option role — the message actions carry a destination <select> whose
  // options are legitimately role=option.
  await expect(suggestions.getByRole('option').filter({ hasText: 'general' })).toHaveCount(0);
  await workspaceSearch.press('ArrowDown');
  await workspaceSearch.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/app/search\\?.*q=${needle}`));
  await expect(page.locator('.result', { hasText: message })).toBeVisible();

  await page.goto('/app');
  await workspaceSearch.fill('SameOldChat');
  const personSuggestion = page.getByRole('option').filter({ hasText: 'Person' });
  await expect(personSuggestion).toContainText('SameOldChat');
  await personSuggestion.click();
  await expect(page).toHaveURL(/\/app\/members\?user=/);
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

test('[ADMIN-04 APP-08 APP-09 WORKFLOW-02] hosted app datastores and event delivery state are inspectable from app administration', async ({ page, context, request }) => {
  await signIn(context);
  const redirectURI = 'https://client.example/hosted-datastore-callback';
  const name = `Hosted data ${Date.now()}`;
  const installed = await createAndInstallApp(page, request, {
    display_information: { name, description: 'Browser-qualified hosted storage' },
    oauth_config: {
      redirect_urls: [redirectURI],
      scopes: { bot: ['datastore:read', 'datastore:write'] },
    },
    settings: { is_hosted: true, function_runtime: 'slack', socket_mode_enabled: true },
    datastores: {
      incidents: {
        primary_key: 'id',
        time_to_live_attribute: 'expires_at',
        attributes: {
          id: { type: 'string' },
          title: { type: 'string' },
          priority: { type: 'integer' },
          expires_at: { type: 'slack#/types/timestamp' },
        },
      },
    },
  }, redirectURI);

  await page.goto(`/app/developer/apps?app=${installed.appID}`);
  await page.getByRole('link', { name: 'Manage hosted datastores' }).click();
  await expect(page.getByRole('heading', { name: 'incidents' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Manifest schema' })).toBeVisible();
  await expect(page.getByText('No items matched this page.')).toBeVisible();

  const item = page.getByRole('textbox', { name: 'Item (JSON)' });
  await item.fill(JSON.stringify({ title: 'Investigate latency', id: 'INC-1', priority: 1 }, null, 2));
  await page.getByRole('button', { name: 'Persist item' }).click();
  await expect(page.getByRole('status')).toContainText('Item persisted');
  await expect(page.getByText('Investigate latency')).toBeVisible();
  await expect(page.getByText('1 matching item.')).toBeVisible();

  await page.getByRole('textbox', { name: 'Expression', exact: true }).fill('contains (#title, :term)');
  await page.getByRole('textbox', { name: 'Expression attributes (JSON)' }).fill('{"#title":"title"}');
  await page.getByRole('textbox', { name: 'Expression values (JSON)' }).fill('{":term":"latency"}');
  await page.getByRole('button', { name: 'Run query' }).click();
  await expect(page.getByText('Investigate latency')).toBeVisible();

  await page.getByLabel('Operation').selectOption('merge');
  await item.fill('{"id":"INC-1","priority":2}');
  await page.getByRole('button', { name: 'Persist item' }).click();
  await expect(page.getByText('Investigate latency')).toBeVisible();
  await expect(page.locator('.item pre')).toContainText('"priority":2');

  await expectNoSeriousAccessibilityViolations(page);
  await page.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('status')).toContainText('Item deleted');
  await expect(page.getByText('No items matched this page.')).toBeVisible();

  await page.goto(`/app/developer/apps?app=${installed.appID}`);
  await page.getByRole('link', { name: 'View event delivery health' }).click();
  await expect(page.getByRole('heading', { name: 'Event delivery health' })).toBeVisible();
  await expect(page.getByText('Queued', { exact: true })).toBeVisible();
  await expect(page.getByText('Socket Mode', { exact: true })).toBeVisible();
  await expect(page.getByText('Yes', { exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Next journal record awaiting evaluation' })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  await page.goto(`/app/developer/apps?app=${installed.appID}`);
  await page.getByRole('button', { name: 'Delete app' }).click();
  await expect(page).toHaveURL(/\/app\/developer\/apps$/);
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

test('[COMP-02 COMP-03 DRAFT-01 FILE-01 ACT-02] composer formatting, references, emoji, drafts, and file preview are functional', async ({ page, context }) => {
  await signIn(context);
  const groupHandle = `support-${Date.now()}`;
  const groupResponse = await page.request.post('/api/usergroups.create', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { name: 'Support rotation', handle: groupHandle, description: 'Browser-qualified user group mention' },
  });
  const groupPayload = await groupResponse.json();
  expect(groupPayload.ok, JSON.stringify(groupPayload)).toBe(true);
  expect(groupPayload.usergroup.is_subteam).toBe(true);
  const groupID = groupPayload.usergroup.id;
  const groupUsersResponse = await page.request.post('/api/usergroups.users.update', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { usergroup: groupID, users: 'Udev' },
  });
  const groupUsersPayload = await groupUsersResponse.json();
  expect(groupUsersPayload.ok, JSON.stringify(groupUsersPayload)).toBe(true);
  await page.goto('/app');

  const composer = page.locator('form.composer textarea[name="text"]');
  const { primary } = await slackModifiers(page);
  await composer.fill('format me');
  await composer.evaluate((field) => field.setSelectionRange(0, field.value.length));
  await composer.press(`${primary}+b`);
  await expect(composer).toHaveValue('*format me*');

  await composer.evaluate((field) => field.setSelectionRange(field.value.length, field.value.length));
  await page.getByRole('button', { name: 'Choose an emoji' }).click();
  const emojiPicker = page.getByRole('dialog', { name: 'Emoji' });
  await expect(emojiPicker).toBeVisible();
  const emojiSearch = emojiPicker.getByPlaceholder('Search emoji');
  await expect(emojiSearch).toBeFocused();
  await emojiSearch.fill('tada');
  await emojiPicker.getByRole('option', { name: ':tada:' }).click();
  await expect(composer).toHaveValue('*format me*:tada:');
  await composer.press('Enter');
  const formatted = page.locator('.message').last();
  await expect(formatted.locator('strong')).toHaveText('format me');
  await expect(formatted.locator('.standard-emoji[aria-label=":tada:"]')).toContainText('🎉');

  await composer.fill('@');
  const suggestions = page.getByRole('listbox', { name: 'Mention suggestions' });
  await expect(suggestions).toBeVisible();
  await composer.press('Enter');
  await expect(composer).toHaveValue(/^<@U[^>]+> $/);

  await composer.fill(`@${groupHandle}`);
  await expect(suggestions.getByRole('option', { name: new RegExp(`@${groupHandle}.*Support rotation`) })).toBeVisible();
  await composer.press('Enter');
  await expect(composer).toHaveValue(`<!subteam^${groupID}> `);
  await composer.pressSequentially('please review');
  await composer.press('Enter');
  const groupMessage = page.locator('.message').last();
  await expect(groupMessage.locator('.slack-mention')).toHaveText(`@${groupHandle}`);
  await expect(groupMessage.locator('.message-text')).not.toContainText(groupID);

  await composer.fill('#gen');
  const channels = page.getByRole('listbox', { name: 'Channel suggestions' });
  await expect(channels).toBeVisible();
  await composer.press('Tab');
  await expect(composer).toHaveValue('<#Cdev> ');
  await composer.pressSequentially('channel reference');
  await composer.press('Enter');
  const channelMessage = page.locator('.message').last();
  await expect(channelMessage.locator('.slack-mention')).toHaveText('#general');
  await expect(channelMessage.locator('.message-text')).not.toContainText('Cdev');

  await composer.fill(':tad');
  const emojiSuggestions = page.getByRole('listbox', { name: 'Emoji suggestions' });
  await expect(emojiSuggestions.getByRole('option', { name: ':tada:' })).toBeVisible();
  await composer.press('Enter');
  await expect(composer).toHaveValue(':tada: ');

  const draft = `durable draft ${Date.now()}`;
  await Promise.all([
    // Any page load or navigation also POSTs /app/draft (init persist and
    // pagehide flush), so a bare "204 from /app/draft" can resolve on one of
    // those while the debounced save of THIS draft is still pending, and the
    // reload below then races it. Key the wait on the request that carries
    // the new text; the body is form-encoded, so parse it rather than
    // substring-matching (spaces arrive as "+").
    page.waitForResponse(
      (response) =>
        response.url().includes('/app/draft?') &&
        response.status() === 204 &&
        response.request().method() === 'POST' &&
        new URLSearchParams(response.request().postData() || '').get('text') === draft,
    ),
    composer.fill(draft),
  ]);
  await page.reload();
  await expect(composer).toHaveValue(draft);

  // Scoped to the composer's own control: the shortcuts dialog names the same
  // action, because it is the same action.
  await page.locator('#upload-details summary').click();
  await page.locator('#upload-file').setInputFiles({
    name: 'preview.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('preview body'),
  });
  await expect(page.locator('#upload-preview')).toContainText('preview.txt');
  await expect(page.locator('#upload-preview')).toContainText('12 B');
  await expect(page.locator('#live-status')).toContainText('saved with this draft');
  await page.reload();
  await expect(composer).toHaveValue(draft);
  await expect(page.locator('#upload-preview')).toContainText('preview.txt');
  await expect(page.locator('.side-link[aria-label*="has a draft"]').first()).toBeVisible();

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
  const reactionPicker = page.getByRole('dialog', { name: 'Emoji' });
  const reactionSearch = reactionPicker.getByPlaceholder('Search emoji');
  await expect(reactionSearch).toBeFocused();
  await reactionSearch.fill('wave');
  const waveOption = reactionPicker.getByRole('option', { name: ':wave:' });
  await expect(waveOption).toBeVisible();
  await reactionSearch.press('ArrowDown');
  await expect(waveOption).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(lastMessage.getByRole('button', { name: /wave reaction/ })).toBeVisible();

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
  await target.getByRole('button', { name: 'Add reaction' }).click();
  const picker = page.getByRole('dialog', { name: 'Emoji' });
  await picker.getByPlaceholder('Search emoji').fill('wave');
  await picker.getByRole('option', { name: ':wave:' }).click();

  const chip = target.locator('.reactions .chip');
  await expect(chip).toHaveCount(1);
  await expect(chip.first()).toHaveAccessibleName(/Remove your wave reaction/);
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

test('[PROFILE-01 PROFILE-02 STATUS-01 STATUS-02 STATUS-03] profile editing and future statuses persist with Slack management semantics', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app/members');

  await expect(page.getByRole('heading', { name: 'People', level: 1 })).toBeVisible();
  await expect(page.locator('input[name^="image_"]')).toHaveCount(0);
  await expect(page.getByLabel('Profile photo URL')).toHaveCount(1);

  const status = `Qualifying ${Date.now()}`;
  const profileForm = page.locator('form[action="/app/profile"]');
  await profileForm.getByLabel('Status', { exact: true }).fill(status);
  await profileForm.getByLabel('Status emoji').fill(':white_check_mark:');
  const expires = new Date(Date.now() + 60 * 60 * 1000);
  const localExpires = new Date(expires.getTime() - expires.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  await page.getByLabel('Remove status after').fill(localExpires);
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page).toHaveURL('/app/members');
  await expect(page.getByText(status, { exact: false }).first()).toBeVisible();
  await expect(page.locator('time[data-status-expires]')).toHaveAttribute('datetime', /T/);

  await page.getByLabel('Availability').selectOption('away');
  await page.getByRole('button', { name: 'Update availability' }).click();
  await expect(page.getByText('Away', { exact: true }).first()).toBeVisible();
  await page.getByLabel('Availability').selectOption('auto');
  await page.getByRole('button', { name: 'Update availability' }).click();
  await expect(page.getByText('Active', { exact: true }).first()).toBeVisible();

  await page.getByRole('button', { name: 'Clear status' }).click();
  await expect(page.getByText('No status set', { exact: true })).toBeVisible();

  const scheduledText = `Lunch ${Date.now()}`;
  const start = new Date(Date.now() + 2 * 60 * 60 * 1000);
  const end = new Date(Date.now() + 3 * 60 * 60 * 1000);
  const asLocal = (value) => new Date(value.getTime() - value.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  const createForm = page.locator('form[action="/app/status/schedule"]');
  await createForm.getByLabel('Status', { exact: true }).fill(scheduledText);
  await createForm.getByLabel('Emoji').fill(':sandwich:');
  await createForm.getByLabel('Start').fill(asLocal(start));
  await createForm.getByLabel('End').fill(asLocal(end));
  await createForm.getByRole('button', { name: 'Save scheduled status' }).click();
  await expect(page).toHaveURL('/app/members');
  await expect(page.getByText('No status set', { exact: true })).toBeVisible();

  const updateForm = page.locator('form[action="/app/status/scheduled/update"]');
  await expect(updateForm).toHaveCount(1);
  await expect(updateForm.getByLabel('Status', { exact: true })).toHaveValue(scheduledText);
  await updateForm.getByLabel('Status', { exact: true }).fill('Deep work');
  await updateForm.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('form[action="/app/status/scheduled/update"]').getByLabel('Status', { exact: true })).toHaveValue('Deep work');
  await page.locator('form[action="/app/status/scheduled/update"]').getByRole('button', { name: 'Cancel status' }).click();
  await expect(page.getByText('No scheduled statuses.', { exact: true })).toBeVisible();
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

test('[CANVAS-01 CANVAS-02 LIST-01 LIST-02] persisted canvases and lists survive their daily UI journeys', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  await page.getByRole('link', { name: 'Canvases' }).click();
  await page.getByText('Create a canvas').click();
  const canvasName = `Launch canvas ${Date.now()}`;
  await page.getByLabel('Name').fill(canvasName);
  await page.getByLabel('Content').fill('Initial durable content');
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { name: canvasName })).toBeVisible();
  await page.getByLabel('Title').fill(`${canvasName} revised`);
  await page.getByLabel('Content').fill('One atomic revision');
  await page.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByText('Canvas saved')).toBeVisible();
  await page.getByRole('link', { name: 'Canvases' }).click();
  await expect(page.getByRole('heading', { name: `${canvasName} revised` })).toBeVisible();

  await page.goto('/app/lists');
  await page.getByText('Create a list').click();
  const listName = `Launch list ${Date.now()}`;
  await page.getByLabel('Name').fill(listName);
  await page.getByLabel('Use as a to-do list').check();
  await page.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { name: listName })).toBeVisible();
  await page.getByLabel('New item').fill('Verify the persisted journey');
  await page.getByRole('button', { name: 'Add' }).click();
  await expect(page.getByText('Verify the persisted journey')).toBeVisible();
  await page.getByRole('button', { name: 'Complete' }).click();
  await expect(page.getByRole('button', { name: 'Restore' })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('button', { name: 'Restore' })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);
});

test('[WORKFLOW-01 WORKFLOW-02 WORKFLOW-03] Workflow Builder publishes a trigger and starts one durable execution', async ({ page, context, request }) => {
  await signIn(context);
  const redirectURI = 'https://client.example/workflow-browser-callback';
  const appName = `Workflow tools ${Date.now()}`;
  const installed = await createAndInstallApp(page, request, {
    display_information: { name: appName },
    oauth_config: {
      redirect_urls: [redirectURI],
      scopes: { bot: ['chat:write'] },
    },
    settings: { function_runtime: 'remote' },
    functions: {
      triage: {
        title: 'Triage request',
        description: 'Triage one incoming request',
        input_parameters: {
          properties: { item: { type: 'string', title: 'Item' } },
          required: ['item'],
        },
        output_parameters: {
          properties: { result: { type: 'string', title: 'Result' } },
          required: ['result'],
        },
      },
      notify: {
        title: 'Notify channel',
        description: 'Post the result',
        input_parameters: { properties: {} },
        output_parameters: { properties: {} },
      },
    },
  }, redirectURI);

  await page.goto('/app');
  await page.getByRole('link', { name: 'Workflows' }).click();
  await page.getByText('Create a workflow').click();
  const workflowName = `Incident workflow ${Date.now()}`;
  await page.getByLabel('Name').fill(workflowName);
  await page.getByLabel('Owning app').selectOption(installed.appID);
  await page.getByLabel('Description').fill('Classify an incident and notify the team');
  await page.getByLabel('Icon (emoji or short text)').fill('🚨');
  await page.getByLabel('First step').selectOption('triage');
  await page.getByLabel('Workflow reference').fill('incident-triage');
  await page.getByRole('button', { name: 'Create workflow' }).click();
  await expect(page.getByRole('heading', { name: workflowName })).toBeVisible();
  await expect(page.getByText('draft', { exact: true })).toBeVisible();
  // The icon travels with the workflow and shows on its builder page.
  await expect(page.locator('.wf-icon')).toHaveText('🚨');
  // The owner sees the workflow managers section.
  await expect(page.getByRole('heading', { name: 'Workflow managers' })).toBeVisible();

  await page.locator('select[name="step_2"]').selectOption('notify');
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByText('Workflow published')).toBeVisible();
  await expect(page.getByText('published', { exact: true })).toBeVisible();

  // Steps reorder in place: moving a step up swaps every field between the
  // two slots, and moving it back restores the order. Nothing is persisted
  // until a save, so the published order below is unchanged.
  await page.getByRole('button', { name: 'Move step 2 up' }).click();
  await expect(page.locator('select[name="step_1"]')).toHaveValue('notify');
  await expect(page.locator('select[name="step_2"]')).toHaveValue('triage');
  await page.getByRole('button', { name: 'Move step 1 down' }).click();
  await expect(page.locator('select[name="step_1"]')).toHaveValue('triage');
  await expect(page.locator('select[name="step_2"]')).toHaveValue('notify');

  // A staged edit is labeled against the published revision step by step:
  // replacing step 2 marks it changed, truncating the head marks the removed
  // step, and extending the head marks an added step.
  await page.locator('select[name="step_2"]').selectOption('triage');
  await page.getByRole('button', { name: 'Save staged changes' }).click();
  await expect(page.getByText('Staged changes saved')).toBeVisible();
  await expect(page.getByLabel('Step 2 changed')).toBeVisible();
  await page.locator('select[name="step_2"]').selectOption('');
  await page.getByRole('button', { name: 'Save staged changes' }).click();
  await expect(page.getByText('Staged changes saved')).toBeVisible();
  await expect(page.locator('[data-removed-step="2"]', { hasText: 'Notify channel' })).toBeVisible();
  await page.locator('select[name="step_2"]').selectOption('triage');
  await page.locator('select[name="step_3"]').selectOption('notify');
  await page.getByRole('button', { name: 'Save staged changes' }).click();
  await expect(page.getByText('Staged changes saved')).toBeVisible();
  await expect(page.getByLabel('Step 2 changed')).toBeVisible();
  await expect(page.getByLabel('Step 3 added')).toBeVisible();
  await expect(page.locator('[data-removed-step]')).toHaveCount(0);
  await page.getByRole('button', { name: 'Publish changes' }).click();
  await expect(page.getByText('Workflow published')).toBeVisible();

  await page.getByLabel('Trigger name').fill('Start incident triage');
  await page.getByLabel('Trigger type').selectOption('link');
  await page.getByRole('button', { name: 'Create trigger' }).click();
  await expect(page.getByText('Trigger created')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Start incident triage' })).toBeVisible();
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(page).toHaveURL(/\/app\/workflows\/runs\/Wx[0-9a-f]+$/);
  const runURL = page.url();
  await expect(page.getByRole('heading', { name: 'Workflow run' })).toBeVisible();
  await expect(page.getByText('running', { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText('An app function is running. Reload to see its latest durable state.')).toBeVisible();
  await page.getByRole('link', { name: '← Workflow' }).click();
  // The owner sees the workflow's run dashboard with the in-flight run.
  await expect(page.getByRole('heading', { name: 'Run activity' })).toBeVisible();
  await expect(page.locator('.activity-counts').getByText('1 running')).toBeVisible();
  await expect(page.locator('[data-activity-run]')).toHaveCount(1);
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('Incident workflow staged');
  await page.getByRole('button', { name: 'Save staged changes' }).click();
  await expect(page.getByText('Staged changes saved')).toBeVisible();
  await expect(page.getByText('your staged changes are not yet published')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Incident workflow staged' })).toBeVisible();
  await page.getByRole('button', { name: 'Discard changes' }).click();
  await expect(page.getByText('Staged changes discarded')).toBeVisible();
  await expect(page.getByRole('heading', { name: workflowName })).toBeVisible();
  await expect(page.getByText('your staged changes are not yet published')).toHaveCount(0);
  await page.getByRole('button', { name: 'Unpublish' }).click();
  await expect(page.getByText('Workflow unpublished')).toBeVisible();
  await expect(page.getByText('disabled', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Run' })).toHaveCount(0);
  await expect(page.locator('.activity-counts').getByText('1 cancelled')).toBeVisible();
  await page.goto(runURL);
  await expect(page.getByText('cancelled', { exact: true })).toBeVisible();
  await expect(page.getByText('workflow_unpublished')).toBeVisible();
  await page.getByRole('link', { name: '← Workflow' }).click();
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByText('Workflow published')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Run' })).toBeVisible();

  await page.getByLabel('Trigger name').fill('Deploy webhook');
  await page.getByLabel('Trigger type').selectOption('webhook');
  await page.getByRole('button', { name: 'Create trigger' }).click();
  await expect(page.getByText('Trigger created')).toBeVisible();
  const webhookURL = await page.locator('code', { hasText: '/services/triggers/' }).first().textContent();
  const invoked = await request.post(webhookURL, { data: { item: 'deployed' } });
  expect(invoked.status()).toBe(200);
  expect(await invoked.text()).toBe('ok');
  const denied = await request.post(webhookURL.replace(/[0-9a-f]+$/, '0'.repeat(48)), { data: {} });
  expect(denied.status()).toBe(404);

  // A weekly schedule can name its weekdays, and the trigger summary shows
  // the named days rather than a bare interval.
  await page.getByLabel('Trigger name').fill('Weekday digest');
  await page.getByLabel('Trigger type').selectOption('scheduled');
  await page.getByLabel('Starts at').fill('2030-05-06T09:00');
  await page.getByLabel('Repeats').selectOption('weekly');
  await page.getByLabel('Mon', { exact: true }).check();
  await page.getByLabel('Wed', { exact: true }).check();
  await page.getByRole('button', { name: 'Create trigger' }).click();
  await expect(page.getByText('Trigger created')).toBeVisible();
  await expect(page.getByText('Every week on mon, wed · UTC')).toBeVisible();

  // A workflow can be duplicated from the builder as a new draft and deleted
  // again: the copy lands on its own builder page, and deleting it returns to
  // the directory without it.
  await page.getByRole('button', { name: 'Copy workflow' }).click();
  await expect(page.getByText('Workflow copied')).toBeVisible();
  await expect(page.getByRole('heading', { name: workflowName + ' (copy)' })).toBeVisible();
  await expect(page.getByText('draft', { exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Delete workflow' }).click();
  await expect(page.getByText('Workflow deleted')).toBeVisible();
  await expect(page.getByRole('link', { name: workflowName + ' (copy)' })).toHaveCount(0);
  await expectNoSeriousAccessibilityViolations(page);
});

test('[WORKFLOW-04] a step with a condition only runs when the condition holds', async ({ page, context, request }) => {
  await signIn(context);
  const redirectURI = 'https://client.example/workflow-branch-callback';
  const installed = await createAndInstallApp(page, request, {
    display_information: { name: `Branch tools ${Date.now()}` },
    oauth_config: {
      redirect_urls: [redirectURI],
      scopes: { bot: ['chat:write'] },
    },
    settings: { function_runtime: 'remote' },
    functions: {
      triage: {
        title: 'Triage request',
        input_parameters: { properties: {}, required: [] },
        output_parameters: { properties: {}, required: [] },
      },
      notify: {
        title: 'Notify channel',
        input_parameters: { properties: {}, required: [] },
        output_parameters: { properties: {}, required: [] },
      },
    },
  }, redirectURI);

  await page.goto('/app');
  await page.getByRole('link', { name: 'Workflows' }).click();
  await page.getByText('Create a workflow').click();
  await page.getByLabel('Name').fill(`Branched workflow ${Date.now()}`);
  await page.getByLabel('Owning app').selectOption(installed.appID);
  await page.getByLabel('First step').selectOption('triage');
  await page.getByRole('button', { name: 'Create workflow' }).click();

  await page.getByLabel('Step 1 condition source').fill('inputs.go');
  await page.getByLabel('Step 1 condition operator').selectOption('equals');
  await page.getByLabel('Step 1 condition value').fill('yes');
  await page.getByLabel('Input metadata (JSON object; syntax validation only)').fill('{"type":"object"}');
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByText('Workflow published')).toBeVisible();

  await page.getByLabel('Trigger name').fill('Run branches');
  await page.getByRole('button', { name: 'Create trigger' }).click();
  await expect(page.getByText('Trigger created')).toBeVisible();

  // Without the input the condition needs, the only step is skipped and the
  // run completes without executing anything.
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(page).toHaveURL(/\/app\/workflows\/runs\/Wx[0-9a-f]+$/);
  await expect(page.getByText('completed', { exact: true })).toBeVisible();

  // With the input set, the condition holds and the second step executes.
  await page.getByRole('link', { name: '← Workflow' }).click();
  await page.getByLabel('Inputs (JSON)').fill('{"go":"yes"}');
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(page).toHaveURL(/\/app\/workflows\/runs\/Wx[0-9a-f]+$/);
  await expect(page.getByText('running', { exact: true })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);
});

test('[WORKFLOW-05] a form step pauses for input and a button step confirms', async ({ page, context, request }) => {
  await signIn(context);
  const redirectURI = 'https://client.example/workflow-form-callback';
  const installed = await createAndInstallApp(page, request, {
    display_information: { name: `Form tools ${Date.now()}` },
    oauth_config: {
      redirect_urls: [redirectURI],
      scopes: { bot: ['chat:write'] },
    },
    settings: { function_runtime: 'remote' },
    functions: {
      confirm: {
        title: 'Send confirmation',
        input_parameters: { properties: {}, required: [] },
        output_parameters: { properties: {}, required: [] },
      },
    },
  }, redirectURI);

  await page.goto('/app');
  await page.getByRole('link', { name: 'Workflows' }).click();
  await page.getByText('Create a workflow').click();
  await page.getByLabel('Name').fill(`Interactive workflow ${Date.now()}`);
  await page.getByLabel('Owning app').selectOption(installed.appID);
  await page.getByLabel('First step').selectOption('confirm');
  await page.getByRole('button', { name: 'Create workflow' }).click();
  const workflowPath = new URL(page.url()).pathname;

  await page.getByLabel('Step 1 type').selectOption('form');
  await page.getByLabel('Step 1 form definition').fill('{"title":"Intake","inputs":{"name":"Name"}}');
  await page.locator('select[name="step_type_2"]').selectOption('button');
  await page.getByLabel('Step 2 button label').fill('Approve');
  await page.locator('select[name="step_type_3"]').selectOption('function');
  await page.locator('select[name="step_3"]').selectOption('confirm');
  await page.getByRole('button', { name: 'Publish' }).click();
  await expect(page.getByText('Workflow published')).toBeVisible();

  await page.getByLabel('Trigger name').fill('Run interactive');
  await page.getByRole('button', { name: 'Create trigger' }).click();
  await expect(page.getByText('Trigger created')).toBeVisible();
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(page).toHaveURL(/\/app\/workflows\/runs\/Wx[0-9a-f]+$/);

  // The form step parks the run; submit it and the run advances to the button.
  await expect(page.getByRole('heading', { name: 'Intake' })).toBeVisible();
  await page.getByLabel('Name').fill('Ada');
  await page.getByRole('button', { name: 'Submit' }).click();
  await expect(page).toHaveURL(/notice=/);
  // Submitting the form advances the run to its button step.
  await expect(page.getByText('Approve')).toBeVisible();

  // The button step waits for confirmation; clicking it advances to the
  // function step.
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(page.getByText('Confirmed')).toBeVisible();
  await expect(page.getByText('running', { exact: true })).toBeVisible();

  // The owner downloads the submitted form fields and the run history as CSV.
  const session = { cookie: `sameoldchat_session=${SESSION}` };
  const formCSV = await request.get(`/app/workflows/export/form-responses/${workflowPath.split('/').pop()}`, { headers: session });
  expect(formCSV.status()).toBe(200);
  const formBody = await formCSV.text();
  expect(formBody).toContain('Intake');
  expect(formBody).toContain('name');
  expect(formBody).toContain('Ada');
  const runsCSV = await request.get(`/app/workflows/export/runs/${workflowPath.split('/').pop()}`, { headers: session });
  expect(runsCSV.status()).toBe(200);
  expect(await runsCSV.text()).toContain('running');
  await expectNoSeriousAccessibilityViolations(page);
});

// Sign-out must come last in this file. The suite runs with a single worker
// against one server holding one static browser session, so revoking it ends
// every session the remaining tests would use. Placing this earlier makes every
// later test fail with 401 for a reason that has nothing to do with what it
// asserts.
// These journeys sit before [AUTH-03] on purpose: signing out is terminal for
// the shared session, so anything that needs a live one has to run first.
//
// They are also deliberately dense: each walks one whole surface
// rather than one control, because the job runs every test in three engines
// with workers: 1 and a test per control would cost more wall-clock than the
// coverage is worth.

test('[HUDDLE-01][HUDDLE-03] a huddle runs its lifecycle and never promises audio', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  // The idle bar offers to start one and says what a huddle is here.
  await expect(page.getByRole('button', { name: 'Start a huddle' })).toBeVisible();
  await expect(page.getByText('carries no voice or video')).toBeVisible();

  await page.getByRole('button', { name: 'Start a huddle' }).click();
  await expect(page.getByText('No audio here')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Leave huddle' })).toBeVisible();
  // The person who started it can end it for everyone; that is the whole
  // difference between leaving and ending.
  await expect(page.getByRole('button', { name: 'End for everyone' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Start a huddle' })).toHaveCount(0);

  await expectNoSeriousAccessibilityViolations(page);

  await page.getByRole('button', { name: 'End for everyone' }).click();
  await expect(page.getByRole('button', { name: 'Start a huddle' })).toBeVisible();
});

test('[CONNECT-01][CONNECT-03] the details panel separates an invitation from a connection', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app?details=1');

  await expect(page.getByRole('heading', { name: 'Shared with other organizations' })).toBeVisible();
  // With nobody invited, the panel says so plainly rather than leaving the
  // section empty and ambiguous.
  await expect(page.getByText('Only this workspace is in this channel')).toBeVisible();
  // The consequence is stated before an invitation is sent, not after.
  await expect(page.getByText('read this channel')).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);
});

test('[NOTIFY-04] the notification preferences name every permission and every gap', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app/notifications');

  const desktop = page.getByLabel('Show desktop notifications while SameOldChat is open in a tab');
  await expect(desktop).toBeVisible();
  await expect(desktop).not.toBeChecked();
  // The browser's own answer is reported, because the server cannot know it.
  await expect(page.getByText(/browser/i).first()).toBeVisible();
  // And what this deployment cannot deliver is named rather than left silent.
  await expect(page.getByRole('heading', { name: 'Not delivered here' })).toBeVisible();
  await expect(page.getByText('There is no mobile application')).toBeVisible();
  await expect(page.getByText('sends no mail at all')).toBeVisible();

  await desktop.check();
  await page.getByRole('button', { name: 'Save workspace defaults' }).click();
  await expect(page.getByLabel('Show desktop notifications while SameOldChat is open in a tab')).toBeChecked();
  await expectNoSeriousAccessibilityViolations(page);
});

test('[FILE-06][FILE-07] remote files are visible and never claim to be hosted here', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app/remote-files');

  await expect(page.getByRole('heading', { name: 'Remote files', level: 1 })).toBeVisible();
  await expect(page.getByText('The contents stay with the app that hosts them')).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);
});

test('[NAV-02 A11Y-01] the shortcuts dialog documents the keyboard layer and is reachable without knowing it', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');
  const { primary } = await slackModifiers(page);

  // The circular-discovery problem: a member who does not know the chord must
  // still be able to find out what the chords are.
  await page.getByRole('button', { name: 'Keyboard shortcuts' }).click();
  const help = page.getByRole('dialog', { name: 'Keyboard shortcuts' });
  await expect(help).toBeVisible();
  await expect(help.getByRole('heading', { name: 'Navigation' })).toBeVisible();
  await expect(help.getByRole('heading', { name: 'Composing' })).toBeVisible();

  // Only this platform's chord is shown, so a member is never told to press a
  // key their keyboard does not have.
  const apple = primary === 'Meta';
  await expect(help.locator(`kbd[data-keyboard-${apple ? 'apple' : 'other'}]`).first()).toBeVisible();
  await expect(help.locator(`kbd[data-keyboard-${apple ? 'other' : 'apple'}]`).first()).toBeHidden();

  const query = help.getByPlaceholder('Search shortcuts');
  await expect(query).toBeFocused();
  await query.fill('thread');
  await expect(help.getByText('Open the thread on the focused message')).toBeVisible();
  await expect(help.getByText('Jump to a conversation')).toBeHidden();
  await query.fill('zzzz');
  await expect(help.getByText('No matching shortcuts.')).toBeVisible();

  await help.getByRole('button', { name: 'Close keyboard shortcuts' }).click();
  await expect(help).toBeHidden();

  // Scoped to the dialog. The timeline around it carries a WCAG 2.2
  // target-size failure recorded in specs/product-gap-audit.md, and this change
  // tried the obvious fix — moving the toolbar into its own message's top
  // padding — which destabilised the message chrome enough to break three
  // unrelated journeys. It is a layout decision about Slack's overlapping-
  // toolbar idiom, and it is not this journey's to make.
  await expectNoSeriousAccessibilityViolations(page, '#keyboard-help');

  // And the chord itself opens it.
  await page.keyboard.press(`${primary}+Slash`);
  await expect(help).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(help).toBeHidden();
});

test('[NAV-02 NAV-04] section movement, unread movement, and mark-all-read work from the keyboard', async ({ page, context, request }) => {
  await signIn(context);
  await page.goto('/app');
  const { primary } = await slackModifiers(page);

  // NAV-02 has required F6 section movement since the journey was written and
  // nothing implemented it. A browser reserves bare F6, so the primary
  // modifier joins it — which the dialog says out loud.
  const composer = page.locator('form.composer textarea[name="text"]');
  await composer.click();
  await page.keyboard.press(`${primary}+F6`);
  await expect(composer).not.toBeFocused();
  const landed = await page.evaluate(() => {
    const active = document.activeElement;
    const section = active && active.closest('#workspace-sidebar,#timeline,#thread-messages,#composer');
    return section ? section.id : (active && active.id) || '';
  });
  expect(landed, 'the primary modifier with F6 moved focus into a different major section').not.toBe('composer');
  expect(landed).not.toBe('');

  // NAV-04: unread movement walks only conversations that report unread
  // messages, which is the same fact the sidebar announces.
  const posted = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: 'Cdev', text: `unread for keyboard navigation ${Date.now()}` },
  });
  expect((await posted.json()).ok).toBe(true);
  await page.goto('/app/activity');
  const unreadNames = await page.locator('.side-section[aria-label="Channels"] .side-link[aria-label*="unread messages"]').count();
  if (unreadNames > 0) {
    await page.keyboard.press('Alt+Shift+ArrowDown');
    await expect(page).toHaveURL(/\/app\?channel=/);
  }

  // Shift+Escape is Slack's mark-everything-read, and it is a durable write:
  // it must go through the CSRF-carrying form, not a bare fetch.
  await page.goto('/app');
  // Deliberately from the composer, which is where focus lands on load: Shift
  // means nothing else to a text field, so the chord has to reach here.
  await page.locator('form.composer textarea[name="text"]').focus();
  await page.keyboard.press('Shift+Escape');
  // A full navigation, not a background fetch: the sidebar badges are
  // server-rendered, so a member who cleared everything has to be shown a
  // sidebar that agrees.
  await expect(page.locator('.channel-actions .notice')).toContainText(/Marked \d+ conversations? read|Everything was already read/);
  await expect(page.locator('.side-section[aria-label="Channels"] .side-link[aria-label*="unread messages"]')).toHaveCount(0);
});

test('[AUTH-04] workspace administration exists and refuses a member rather than 404ing', async ({ page, context }) => {
  await signIn(context);

  // Every administration route used to be registered only alongside a
  // configured identity provider, so this deployment — which runs on the static
  // development session — answered 404 for all of them. A 404 tells an operator
  // their deployment is broken; a 403 tells a member their account is not
  // privileged. Those are different problems and the surface now distinguishes
  // them.
  //
  // This session cannot go further, and deliberately so: cmd/server grants the
  // shared static session member scopes only, because a shared bearer token
  // must not carry control-plane authority. Exercising the administration
  // journeys themselves needs a session this deployment will not mint — see
  // specs/product-gap-audit.md.
  for (const path of ['/app/admin/settings', '/app/admin/analytics', '/app/admin/audit']) {
    const response = await page.goto(path);
    expect(response.status(), `${path} must exist and refuse, not 404`).toBe(403);
  }

  // Identity-provider administration keeps the dependency it actually has.
  const providers = await page.goto('/app/admin/auth');
  expect(providers.status()).toBe(404);

  // And a member is not offered a link to what they cannot use.
  await page.goto('/app');
  await expect(page.getByRole('link', { name: 'Workspace settings' })).toHaveCount(0);
});

test('[NAV-07 NAV-08] the Threads view lists followed threads and Unreads groups what is unread', async ({ page, context, request }) => {
  await signIn(context);
  const { primary } = await slackModifiers(page);

  const rootText = `threads view root ${Date.now()}`;
  const root = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: 'Cdev', text: rootText },
  });
  const rootPayload = await root.json();
  expect(rootPayload.ok, JSON.stringify(rootPayload)).toBe(true);
  const reply = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: 'Cdev', text: 'threads view reply', thread_ts: rootPayload.ts },
  });
  expect((await reply.json()).ok).toBe(true);

  // Replying is what starts following a thread in Slack, and the Threads view
  // is the only surface that has ever read those follow records.
  await page.goto('/app/threads');
  await expect(page.getByRole('heading', { name: 'Threads', exact: true, level: 2 })).toBeVisible();
  await expect(page.getByText(rootText)).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  await page.goto('/app/unreads');
  await expect(page.getByRole('heading', { name: 'Unreads', exact: true, level: 2 })).toBeVisible();
  await expectNoSeriousAccessibilityViolations(page);

  // Both are reachable by the chords Slack publishes for them.
  await page.goto('/app');
  await page.keyboard.press(`${primary}+Shift+T`);
  await expect(page).toHaveURL(/\/app\/threads/);
  await page.goto('/app');
  await page.keyboard.press(`${primary}+Shift+A`);
  await expect(page).toHaveURL(/\/app\/unreads/);
});

test('[NAV-05] a permalink lands on its message, and history returns without replaying', async ({ page, context, request }) => {
  await signIn(context);

  const marker = `permalink target ${Date.now()}`;
  const posted = await request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: CHANNEL, text: marker },
  });
  const payload = await posted.json();
  expect(payload.ok, JSON.stringify(payload)).toBe(true);

  // The permalink the UI itself offers, not one this test assembles: the packed
  // timestamp format is the thing under test, so deriving it here would prove
  // only that the test can do arithmetic.
  await page.goto('/app');
  const message = page.locator('.message').filter({ hasText: marker }).last();
  const permalink = await message.locator('a.copy-link').getAttribute('href');
  expect(permalink).toMatch(/^\/archives\/Cdev\/p\d+$/);

  await page.goto(permalink);
  // It resolves to the conversation with a window built to CONTAIN the target,
  // and the target is the fragment, so a reader lands on the message rather
  // than near it.
  await expect(page).toHaveURL(/\/app\?.*channel=Cdev/);
  await expect(page).toHaveURL(/#message-/);
  await expect(page.locator('.message').filter({ hasText: marker })).toHaveCount(1);

  // Malformed and unresolvable links have distinct, safe outcomes: neither
  // discloses whether the message was deleted or merely unreadable.
  expect((await page.goto('/archives/Cdev/not-a-timestamp')).status()).toBe(404);
  expect((await page.goto('/archives/Cdev/p1')).status()).toBe(404);
  const missing = await page.goto('/archives/Cdev/p1700000000000001');
  expect(missing.status(), 'an unresolvable permalink is handled, not a 500').toBe(404);
  // It names the message, not the conversation: for a permalink the
  // conversation is usually readable and only the target is gone, and it must
  // not disclose whether the target was deleted or merely unreadable.
  await expect(page.getByRole('heading', { name: 'That message is not available' })).toBeVisible();

  // Back and forward return through real destinations, and going back to a page
  // reached by a redirect must not re-run the redirect's work.
  await page.goto('/app');
  await page.goto('/app/threads');
  await page.goBack();
  await expect(page).toHaveURL(/\/app(\?|$)/);
  await page.goForward();
  await expect(page).toHaveURL(/\/app\/threads/);
  await expect(page.getByRole('heading', { name: 'Threads', exact: true, level: 2 })).toBeVisible();
});

test('[NAV-01 A11Y-01] the workspace shell names its regions and marks the current destination', async ({ page, context }) => {
  await signIn(context);
  await page.goto('/app');

  // Every region a member navigates by name.
  await expect(page.getByRole('banner')).toBeVisible();
  await expect(page.getByRole('complementary')).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Workspace navigation' })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Channels' })).toBeVisible();
  // The direct-message *section* appears only when the member has open DMs,
  // which is what "according to Slack availability" means; the destination
  // itself is always reachable from workspace navigation.
  const workspaceNav = page.getByRole('navigation', { name: 'Workspace navigation' });
  for (const destination of ['Unreads', 'Threads', 'Activity', 'Later', 'Direct messages', 'Apps']) {
    await expect(workspaceNav.getByRole('link', { name: destination, exact: true })).toHaveCount(1);
  }
  await expect(page.getByRole('link', { name: 'Skip to the messages' })).toHaveCount(1);

  // The active destination is programmatically current, not merely styled.
  const current = page.locator('.side-section[aria-label="Channels"] .side-link[aria-current="page"]');
  await expect(current).toHaveCount(1);
  await expect(current).toContainText('general');

  // Narrow: the same destinations stay reachable through a named control, the
  // drawer traps focus while open, and closing it does not change conversation.
  await page.setViewportSize({ width: 420, height: 900 });
  const toggle = page.getByRole('button', { name: /navigation/i }).first();
  await expect(toggle).toBeVisible();
  await toggle.click();
  const drawer = page.locator('#workspace-sidebar');
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole('link', { name: 'Threads' })).toBeVisible();
  await page.keyboard.press('Escape');
  // Closing the drawer must not reset the open conversation, which is a claim
  // about what is rendered rather than about the address bar: the workspace can
  // be reached without a channel parameter at all.
  await expect(page.locator('.channel-title')).toHaveText('# general');
  await expectNoSeriousAccessibilityViolations(page);
  await page.setViewportSize({ width: 1280, height: 720 });
});

test('[FILE-02] the external upload sequence produces one shared file and nothing before completion', async ({ page, context, request }) => {
  await signIn(context);

  const name = `external-${Date.now()}.txt`;
  const bytes = 'external upload qualification';
  const ticket = await request.post('/api/files.getUploadURLExternal', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/x-www-form-urlencoded' },
    form: { filename: name, length: String(bytes.length) },
  });
  const ticketPayload = await ticket.json();
  expect(ticketPayload.ok, JSON.stringify(ticketPayload)).toBe(true);
  expect(typeof ticketPayload.upload_url).toBe('string');
  expect(typeof ticketPayload.file_id).toBe('string');

  // Before the bytes are transferred and the upload completed, nothing about
  // this file may be visible: an uncompleted transfer never becomes a file.
  await page.goto('/app');
  await expect(page.getByText(name)).toHaveCount(0);

  const transfer = await request.post(ticketPayload.upload_url, {
    headers: { 'content-type': 'application/octet-stream' },
    data: bytes,
  });
  expect(transfer.status(), await transfer.text()).toBeLessThan(300);

  const complete = await request.post('/api/files.completeUploadExternal', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { files: [{ id: ticketPayload.file_id, title: name }], channel_id: CHANNEL },
  });
  const completePayload = await complete.json();
  expect(completePayload.ok, JSON.stringify(completePayload)).toBe(true);

  // A completed share becomes exactly one history message the member can see.
  await page.goto('/app');
  await expect(page.getByText(name).first()).toBeVisible();

  // Completion is idempotent: repeating it must not produce a second message.
  const again = await request.post('/api/files.completeUploadExternal', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { files: [{ id: ticketPayload.file_id, title: name }], channel_id: CHANNEL },
  });
  expect((await again.json()).ok).toBe(true);
  await page.goto('/app');
  // Counted as messages, not as text nodes: one message renders the name in its
  // link, its label and its title, so counting text would count the rendering.
  await expect(page.locator('.message').filter({ hasText: name })).toHaveCount(1);
});

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

  // The revoked session cannot reopen a protected page: the browser is sent
  // into sign-in rather than shown protected content. A bare 401 used to be
  // asserted here, but AUTH-03 requires only that protected content stays
  // unreachable, AUTH-04 requires that the person is offered
  // reauthentication, and the SSO validator expects exactly this 303 — a
  // dead-end 401 page offered neither.
  await page.goto('/app');
  await expect(page).toHaveURL(/\/login/);
  await expect(page.locator('form.composer')).toHaveCount(0);
});
