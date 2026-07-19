import { test, expect } from '@playwright/test';

test('workspace supports the core browser journey', async ({ page, context }) => {
  await context.addCookies([
    {
      name: 'sameoldchat_session',
      value: 'browser-session',
      url: 'http://127.0.0.1:18080',
    },
  ]);

  await page.goto('/app');
  await expect(page.locator('.channel-title')).toHaveText('# Cdev');
  const composer = page.locator('form.composer textarea[name="text"]');
  await expect(composer).toBeVisible();

  const message = `browser qualification ${Date.now()}`;
  await composer.fill(message);
  await expect(composer).toHaveValue(message);
  const postRequest = page.waitForRequest((request) =>
    request.url().includes('/app/message') && request.method() === 'POST',
  );
  const postMessage = page.waitForResponse((response) =>
    response.url().includes('/app/message') && response.request().method() === 'POST',
  );
  await page.getByRole('button', { name: 'Send' }).click();
  const requestContentType = await (await postRequest).headerValue('content-type');
  await expect(requestContentType).toMatch(/^multipart\/form-data; boundary=/);
  const postResponse = await postMessage;
  const postBody = await postResponse.text();
  await expect(postResponse.status(), postBody).toBe(200);
  await expect(page.locator('.message-text').last()).toHaveText(message);

  const reaction = page.locator('.message').last().locator('form[aria-label="Add reaction"] input[name="name"]');
  await reaction.fill(':wave:');
  await reaction.press('Enter');
  await expect(page).toHaveURL(/\/app\?channel=Cdev/);
  await expect(page.locator('.message-text').last()).toHaveText(message);

  const search = page.locator('form[aria-label="Search the workspace"] input[name="q"]');
  await search.fill('browser qualification');
  await expect(search).toHaveValue('browser qualification');
  await search.press('Enter');
  await expect(page).toHaveURL(/\/app\/search\?q=browser(?:%20|\+)qualification/);
  await expect(page.getByRole('heading', { name: 'Search results' })).toBeVisible();
  await expect(page.locator('.result').last()).toContainText(message);

  await page.getByRole('button', { name: '☾' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.getByRole('button', { name: '☾' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');

  await page.getByRole('link', { name: 'Back to chat' }).click();
  await expect(page.locator('.channel-title')).toHaveText('# Cdev');
  await page.getByRole('link', { name: 'Members' }).first().click();
  await expect(page.getByRole('heading', { name: 'Workspace members' })).toBeVisible();
});
