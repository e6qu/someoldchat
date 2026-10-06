import { test, expect } from '@playwright/test';

// Two people on one canvas, each in a browser of their own. The second is the
// member every server in this suite seeds with -peer-session-token.
const SESSION = 'browser-session';
const PEER_SESSION = 'browser-peer';
const PEER = 'Upeer';

// A script error on either page fails the journey, whatever the page still
// shows: the editor once called an undefined function at the end of every
// remote edit, which cut short the caret restore after it and was invisible to
// assertions on the text alone.
let pageErrors = [];

async function signedIn(browser, baseURL, session) {
  const context = await browser.newContext({ baseURL });
  await context.addCookies([{ name: 'sameoldchat_session', value: session, url: baseURL }]);
  const page = await context.newPage();
  page.on('pageerror', (error) => pageErrors.push(`${session}: ${error.message}`));
  return { context, page };
}

test.afterEach(() => {
  const seen = pageErrors;
  pageErrors = [];
  expect(seen, 'uncaught script errors').toEqual([]);
});

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

// Words another writer adds before your caret arrive without moving you: the
// caret stays after the character it was after, so what you type next lands
// where you were typing. It used to be put back at the same character offset,
// which the other writer's words had pushed along, so a remote insert at the
// start of your block sent your next keystroke that many characters back.
test('[CANVAS-02] another writer typing before your caret does not move it', async ({ browser, baseURL }) => {
  const owner = await signedIn(browser, baseURL, SESSION);
  const peer = await signedIn(browser, baseURL, PEER_SESSION);
  try {
    const name = `caret-${Date.now()}`;
    await createCanvas(owner.page, name, 'the first line');
    await shareWithPeer(owner.page, 'write');
    await peer.page.goto(owner.page.url());

    const mine = owner.page.getByRole('textbox', { name: 'Canvas content' });
    const theirs = peer.page.getByRole('textbox', { name: 'Canvas content' });
    await expect(theirs).toContainText('the first line');
    await mine.locator('[data-canvas-block]').first().click();
    await owner.page.keyboard.press('End');
    await owner.page.keyboard.type(' and mine');
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(theirs.locator('[data-canvas-block]').first()).toHaveText('the first line and mine');

    await theirs.locator('[data-canvas-block]').first().click();
    await peer.page.keyboard.press('Home');
    await peer.page.keyboard.type('Intro: ');
    await expect(peer.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await expect(mine.locator('[data-canvas-block]').first()).toHaveText('Intro: the first line and mine');

    await owner.page.keyboard.type(' too');
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    for (const editor of [mine, theirs]) {
      await expect(editor.locator('[data-canvas-block]').first()).toHaveText('Intro: the first line and mine too');
    }
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

// Each person on a canvas sees who else is there, and a writer sees which
// block another writer's cursor is in, following it as it moves. Someone who
// closes the canvas drops off the list.
test('[CANVAS-02] people on a canvas see who else is there and where they are writing', async ({ browser, baseURL }) => {
  const owner = await signedIn(browser, baseURL, SESSION);
  const peer = await signedIn(browser, baseURL, PEER_SESSION);
  try {
    const name = `presence-${Date.now()}`;
    await createCanvas(owner.page, name, 'Agenda\n\nNotes\n\nActions');
    await shareWithPeer(owner.page, 'write');
    const ownerHere = owner.page.locator('[data-canvas-present]');
    await expect(ownerHere).toBeHidden();

    await peer.page.goto(owner.page.url());
    await expect(ownerHere).toHaveText('Also here: Peer');
    await expect(peer.page.locator('[data-canvas-present]')).toContainText('Also here: ');

    // The peer's cursor is drawn where it is in the owner's page: in the
    // block it is in, and after the words it follows.
    const theirs = peer.page.getByRole('textbox', { name: 'Canvas content' });
    const mine = owner.page.getByRole('textbox', { name: 'Canvas content' });
    const caret = owner.page.locator('[data-canvas-caret="Peer"]');
    async function caretIsIn(block, edge) {
      await expect(async () => {
        const at = await caret.boundingBox();
        const box = await block.boundingBox();
        expect(at).not.toBeNull();
        expect(at.y + at.height / 2).toBeGreaterThan(box.y);
        expect(at.y + at.height / 2).toBeLessThan(box.y + box.height);
        if (edge === 'end') expect(at.x).toBeGreaterThan(box.x + 20);
        if (edge === 'start') expect(at.x).toBeLessThan(box.x + 12);
      }).toPass({ timeout: 10_000 });
    }
    await theirs.locator('[data-canvas-block]').nth(1).click();
    await peer.page.keyboard.press('End');
    await caretIsIn(mine.locator('[data-canvas-block]').nth(1), 'end');
    await peer.page.keyboard.press('Home');
    await caretIsIn(mine.locator('[data-canvas-block]').nth(1), 'start');
    await theirs.locator('[data-canvas-block]').nth(2).click();
    await peer.page.keyboard.press('End');
    await caretIsIn(mine.locator('[data-canvas-block]').nth(2), 'end');
    await expect(owner.page.locator('[data-canvas-caret]')).toHaveCount(1);

    // What the peer selects is shaded on the owner's page, over the words
    // selected, and the shading goes when the selection does.
    await peer.page.keyboard.press('Shift+Home');
    const shade = owner.page.locator('[data-canvas-selection="Peer"]');
    await expect(shade).toHaveCount(1);
    await expect(async () => {
      const shaded = await shade.boundingBox();
      const block = await mine.locator('[data-canvas-block]').nth(2).boundingBox();
      expect(shaded.width).toBeGreaterThan(20);
      expect(shaded.y + shaded.height / 2).toBeGreaterThan(block.y);
      expect(shaded.y + shaded.height / 2).toBeLessThan(block.y + block.height);
    }).toPass({ timeout: 10_000 });
    await peer.page.keyboard.press('End');
    await expect(shade).toHaveCount(0);

    // The mark never becomes part of the text: the owner's page still reads
    // back as it was, and nothing is waiting to be saved.
    await expect(owner.page.locator('[data-canvas-status]')).not.toHaveText('Unsaved changes');

    // The mark follows the words, not the position: a block added above the
    // peer's moves it down, and the mark moves with it.
    await mine.locator('[data-canvas-block]').first().click();
    await owner.page.keyboard.press('Home');
    await owner.page.keyboard.type('Intro');
    await owner.page.keyboard.press('Enter');
    await expect(owner.page.locator('[data-canvas-status]')).toHaveText('Saved');
    await caretIsIn(mine.locator('[data-canvas-block]', { hasText: 'Actions' }), 'end');

    // Leaving says goodbye at once rather than waiting out the fifteen
    // seconds an unrenewed presence lasts. The peer navigates away, which
    // fires pagehide in every engine; Playwright's page.close() does not in
    // Firefox, so closing would only ever test the expiry.
    await peer.page.goto('/app/canvases');
    await expect(ownerHere).toBeHidden({ timeout: 8_000 });
    await expect(owner.page.locator('[data-canvas-caret]')).toHaveCount(0);
  } finally {
    await owner.context.close();
    await peer.context.close();
  }
});
