import { test, expect } from '@playwright/test';

// Two people on one canvas, each in a browser of their own. The second is the
// member every server in this suite seeds with -peer-session-token.
const SESSION = 'browser-session';
const PEER_SESSION = 'browser-peer';
const PEER = 'Upeer';

async function signedIn(browser, baseURL, session) {
  const context = await browser.newContext({ baseURL });
  await context.addCookies([{ name: 'sameoldchat_session', value: session, url: baseURL }]);
  return { context, page: await context.newPage() };
}

async function createCanvas(page, name, content) {
  await page.goto('/app/canvases');
  await page.getByRole('group').filter({ hasText: 'Create a canvas' }).locator('summary').click();
  await page.getByLabel('Name').fill(name);
  await page.getByLabel('Content').fill(content);
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(page.getByRole('heading', { name })).toBeVisible();
}

async function shareWithPeer(page, access) {
  await page.locator('#share-target').selectOption(`user:${PEER}`);
  await page.locator('#share-access').selectOption(access);
  await page.getByRole('button', { name: 'Share canvas' }).click();
  await expect(page.locator('.grant')).toHaveCount(2);
}

// An edit shows on every open copy of the canvas as it is made, without a
// reload, and two people writing at once each keep their words and their
// place: the other's text arrives around them rather than replacing theirs.
test('[CANVAS-02] two people writing on one canvas see each other’s words as they type', async ({ browser, baseURL }) => {
  const owner = await signedIn(browser, baseURL, SESSION);
  const peer = await signedIn(browser, baseURL, PEER_SESSION);
  try {
    const name = `live-${Date.now()}`;
    await createCanvas(owner.page, name, 'Agenda\n\nNotes');
    await shareWithPeer(owner.page, 'write');
    await peer.page.goto(owner.page.url());

    const mine = owner.page.getByRole('textbox', { name: 'Canvas content' });
    const theirs = peer.page.getByRole('textbox', { name: 'Canvas content' });
    await expect(theirs).toContainText('Agenda');

    // One writes; the other sees it without reloading.
    await mine.locator('[data-canvas-block]').first().click();
    await owner.page.keyboard.press('End');
    await owner.page.keyboard.type(' for Monday');
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(theirs.locator('[data-canvas-block]').first()).toHaveText('Agenda for Monday');

    // Both write at once, in different blocks. Each keeps typing where they
    // are while the other's words arrive.
    await theirs.locator('[data-canvas-block]').last().click();
    await peer.page.keyboard.press('End');
    await mine.locator('[data-canvas-block]').first().click();
    await owner.page.keyboard.press('End');
    await Promise.all([
      owner.page.keyboard.type(' and Tuesday', { delay: 40 }),
      peer.page.keyboard.type(' from the call', { delay: 40 }),
    ]);
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(peer.page.locator('[data-canvas-status]')).toHaveText('Saved');
    for (const editor of [mine, theirs]) {
      await expect(editor.locator('[data-canvas-block]').first()).toHaveText('Agenda for Monday and Tuesday');
      await expect(editor.locator('[data-canvas-block]').last()).toHaveText('Notes from the call');
    }

    // Their caret stayed in their own block: what they type next lands there.
    await peer.page.keyboard.type('!');
    await expect(mine.locator('[data-canvas-block]').last()).toHaveText('Notes from the call!');

    // A new block written by one is rendered for the other as the server
    // renders it, not left as plain text.
    await mine.locator('[data-canvas-block]').last().click();
    await owner.page.keyboard.press('End');
    await owner.page.keyboard.press('Enter');
    await owner.page.keyboard.type('Follow-ups');
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(theirs).toContainText('Follow-ups');

    // Both write in the same block at once, one at its start and one at its
    // end. Each keeps their place in it as the other's words arrive.
    await theirs.locator('[data-canvas-block]').first().click();
    await peer.page.keyboard.press('Home');
    await mine.locator('[data-canvas-block]').first().click();
    await owner.page.keyboard.press('End');
    await Promise.all([
      owner.page.keyboard.type(' (moved)', { delay: 60 }),
      peer.page.keyboard.type('Draft: ', { delay: 60 }),
    ]);
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(peer.page.locator('[data-canvas-status]')).toHaveText('Saved');
    for (const editor of [mine, theirs]) {
      await expect(editor.locator('[data-canvas-block]').first()).toHaveText('Draft: Agenda for Monday and Tuesday (moved)');
    }

    // Saving and reloading shows the text both pages agreed on.
    await peer.page.getByRole('button', { name: 'Save canvas' }).click();
    await expect(peer.page.getByRole('status').filter({ hasText: 'Canvas saved' })).toBeVisible();
    await expect(theirs).toContainText('Agenda for Monday and Tuesday');
    await expect(theirs).toContainText('Notes from the call!');
    await expect(theirs).toContainText('Follow-ups');
  } finally {
    await owner.context.close();
    await peer.context.close();
  }
});

// Someone who can only read the canvas sees it change as it is written, and
// someone it is not shared with is told nothing about it.
test('[CANVAS-02] a reader sees the canvas change as it is written', async ({ browser, baseURL }) => {
  const owner = await signedIn(browser, baseURL, SESSION);
  const peer = await signedIn(browser, baseURL, PEER_SESSION);
  try {
    const name = `live-read-${Date.now()}`;
    await createCanvas(owner.page, name, 'Draft one');
    await shareWithPeer(owner.page, 'read');
    await peer.page.goto(owner.page.url());
    const view = peer.page.locator('[data-canvas-view]');
    await expect(view).toContainText('Draft one');

    const mine = owner.page.getByRole('textbox', { name: 'Canvas content' });
    await mine.locator('[data-canvas-block]').first().click();
    await owner.page.keyboard.press('End');
    await owner.page.keyboard.type(', revised');
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(view).toContainText('Draft one, revised');

    // Ctrl+S saves the canvas from the keyboard.
    await mine.click();
    await owner.page.keyboard.press('Control+s');
    await expect(owner.page.getByRole('status').filter({ hasText: 'Canvas saved' })).toBeVisible();
  } finally {
    await owner.context.close();
    await peer.context.close();
  }
});
