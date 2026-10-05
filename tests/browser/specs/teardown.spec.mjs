import { test, expect } from '@playwright/test';

// What a page still holds when the reader leaves it. WebKit has crashed tearing
// /app down on CI ([NAV-05]) with a live stream open, requests in flight and an
// unconditional keepalive draft save starting at the same instant as the next
// navigation. Every page now lets go of what it holds through one pagehide
// path (pageLifecycleScript); these journeys check that from the browser's own
// objects, recorded by instrumentation installed before any page script runs,
// rather than from the page's own bookkeeping.

const SESSION = 'browser-session';
const PEER_SESSION = 'browser-peer';
const API_TOKEN = 'xoxb-browser';
const PEER = 'Upeer';

// Wraps the objects a page can leave open and, after the page's own pagehide
// handlers have run, writes what is still open to sessionStorage, which the
// next page in the same tab can read.
function instrument() {
  const opened = { streams: [], intervals: new Set(), keepalive: [], beacons: [], connections: [], tracks: [], contexts: [] };
  const NativeEventSource = window.EventSource;
  window.EventSource = function (url, init) {
    const stream = new NativeEventSource(url, init);
    opened.streams.push(stream);
    return stream;
  };
  window.EventSource.prototype = NativeEventSource.prototype;
  Object.assign(window.EventSource, { CONNECTING: 0, OPEN: 1, CLOSED: 2 });
  const nativeSetInterval = window.setInterval.bind(window);
  const nativeClearInterval = window.clearInterval.bind(window);
  window.setInterval = (fn, delay, ...rest) => {
    const id = nativeSetInterval(fn, delay, ...rest);
    opened.intervals.add(id);
    return id;
  };
  window.clearInterval = (id) => {
    opened.intervals.delete(id);
    nativeClearInterval(id);
  };
  const nativeFetch = window.fetch.bind(window);
  window.fetch = (input, init) => {
    if (init && init.keepalive) opened.keepalive.push(String(input));
    return nativeFetch(input, init);
  };
  if (navigator.sendBeacon) {
    const nativeBeacon = navigator.sendBeacon.bind(navigator);
    navigator.sendBeacon = (url, data) => {
      opened.beacons.push(String(url));
      return nativeBeacon(url, data);
    };
  }
  if (window.RTCPeerConnection) {
    const NativePeer = window.RTCPeerConnection;
    window.RTCPeerConnection = function (config) {
      const connection = new NativePeer(config);
      opened.connections.push(connection);
      return connection;
    };
    window.RTCPeerConnection.prototype = NativePeer.prototype;
  }
  if (window.AudioContext) {
    const NativeAudio = window.AudioContext;
    window.AudioContext = function (options) {
      const context = new NativeAudio(options);
      opened.contexts.push(context);
      return context;
    };
    window.AudioContext.prototype = NativeAudio.prototype;
  }
  if (navigator.mediaDevices && navigator.mediaDevices.getUserMedia) {
    const nativeUserMedia = navigator.mediaDevices.getUserMedia.bind(navigator.mediaDevices);
    navigator.mediaDevices.getUserMedia = (constraints) => nativeUserMedia(constraints).then((stream) => {
      stream.getTracks().forEach((track) => opened.tracks.push(track));
      return stream;
    });
  }
  window.__opened = opened;
  window.addEventListener('load', () => {
    window.addEventListener('pagehide', (event) => {
      const report = {
        path: window.location.pathname,
        persisted: event.persisted,
        openStreams: opened.streams.filter((stream) => stream.readyState !== 2).map((stream) => stream.url),
        liveIntervals: opened.intervals.size,
        keepalive: opened.keepalive,
        beacons: opened.beacons,
        connections: opened.connections.length,
        openConnections: opened.connections.filter((connection) => connection.signalingState !== 'closed').length,
        liveTracks: opened.tracks.filter((track) => track.readyState !== 'ended').length,
        runningAudio: opened.contexts.filter((context) => context.state === 'running').length,
      };
      try {
        const reports = JSON.parse(sessionStorage.getItem('teardown-reports') || '[]');
        reports.push(report);
        sessionStorage.setItem('teardown-reports', JSON.stringify(reports));
      } catch (error) {}
    });
  });
}

async function signedIn(browser, baseURL, session) {
  const context = await browser.newContext({ baseURL });
  await context.addCookies([{ name: 'sameoldchat_session', value: session, url: baseURL }]);
  await context.addInitScript(instrument);
  return { context, page: await context.newPage() };
}

async function lastReport(page, path) {
  const reports = await page.evaluate(() => JSON.parse(sessionStorage.getItem('teardown-reports') || '[]'));
  const report = reports.filter((entry) => entry.path === path).pop();
  expect(report, JSON.stringify(reports)).toBeTruthy();
  return report;
}

// Leaving /app the way [NAV-05] does — straight to a permalink, which
// redirects back to /app — closes its live stream, clears its timers, and
// sends nothing for a composer nobody touched.
test('[NAV-05] leaving the workspace lets go of everything it holds', async ({ browser, baseURL, request }) => {
  const { context, page } = await signedIn(browser, baseURL, SESSION);
  try {
    const marker = `teardown target ${Date.now()}`;
    const posted = await request.post('/api/chat.postMessage', {
      headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
      data: { channel: 'Cdev', text: marker },
    });
    expect((await posted.json()).ok).toBe(true);

    await page.goto('/app?channel=Cdev');
    const message = page.locator('.message').filter({ hasText: marker }).last();
    await expect(message).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.__opened.streams.length)).toBeGreaterThan(0);
    const permalink = await message.locator('a.copy-link').getAttribute('href');
    await page.goto(permalink);
    await expect(page).toHaveURL(/#message-/);

    const left = await lastReport(page, '/app');
    expect(left.openStreams).toEqual([]);
    expect(left.liveIntervals).toBe(0);
    expect(left.keepalive, 'an untouched composer sends nothing on the way out').toEqual([]);
    expect(left.beacons).toEqual([]);

    // A draft typed just before leaving is still saved on the way out, once.
    const marker2 = `unsent draft ${Date.now()}`;
    await page.locator('#text-editor').click();
    await page.keyboard.type(marker2);
    await page.goto('/app/threads');
    const typed = await lastReport(page, '/app');
    expect(typed.keepalive.filter((url) => url.includes('/app/draft'))).toHaveLength(1);
    await page.goto('/app/drafts');
    await expect(page.getByText(marker2)).toBeVisible();
  } finally {
    await context.close();
  }
});

// A page the browser restores from its back/forward cache had let go of its
// stream, so it reloads and is live again.
test('[NAV-05] a workspace page restored from history is live again', async ({ browser, baseURL }) => {
  const { context, page } = await signedIn(browser, baseURL, SESSION);
  try {
    await page.goto('/app?channel=Cdev');
    await page.goto('/app/threads');
    await page.goBack();
    await expect(page).toHaveURL(/\/app\?channel=Cdev/);
    await expect.poll(() => page.evaluate(() => window.__opened.streams.filter((stream) => stream.readyState === 1).length), { timeout: 10_000 }).toBeGreaterThan(0);
  } finally {
    await context.close();
  }
});

// A joined huddle keeps one microphone and one connection while its window is
// re-rendered by people joining, and lets go of both when the reader leaves.
test('[HUDDLE-01 NAV-05] a huddle keeps one connection while it changes and releases it on leaving', async ({ browser, baseURL, browserName, request }) => {
  test.skip(browserName !== 'chromium', 'Only Chromium is given a synthetic capture device in this suite');
  const owner = await signedIn(browser, baseURL, SESSION);
  const peer = await signedIn(browser, baseURL, PEER_SESSION);
  try {
    await owner.context.grantPermissions(['microphone', 'camera'], { origin: baseURL });
    await peer.context.grantPermissions(['microphone', 'camera'], { origin: baseURL });
    const name = `teardown-huddle-${Date.now()}`;
    const created = await request.post('/api/conversations.create', {
      headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
      data: { name },
    });
    const { channel } = await created.json();
    const invited = await request.post('/api/conversations.invite', {
      headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
      data: { channel: channel.id, users: PEER },
    });
    expect((await invited.json()).ok).toBe(true);

    await owner.page.goto(`/app?channel=${channel.id}`);
    await owner.page.getByRole('button', { name: 'Huddle', exact: true }).click();
    await owner.page.locator('details[open] > .menu-list').last().getByRole('menuitem', { name: 'Start a huddle' }).click();
    const ownerWindow = owner.page.getByRole('region', { name: `Huddle in #${name}` });
    await expect(ownerWindow).toBeVisible();
    await expect(owner.page.locator('.huddle-media-session[data-huddle-microphone="on"]')).toHaveCount(1);
    await expect.poll(() => owner.page.evaluate(() => window.__opened.connections.length)).toBe(1);

    await peer.page.goto(`/app?channel=${channel.id}`);
    await peer.page.getByRole('button', { name: 'Huddle, in progress', exact: true }).click();
    await peer.page.locator('details[open] > .menu-list').last().getByRole('menuitem', { name: 'Join huddle' }).click();
    await expect(peer.page.getByRole('region', { name: `Huddle in #${name}` })).toBeVisible();

    // The owner's window was re-rendered to name the second person; the call
    // it holds is the same one.
    await expect(ownerWindow.locator('.huddle-people')).toContainText('2 people');
    await expect(owner.page.locator('.huddle-media-session[data-huddle-connected="1"]')).toHaveCount(1);
    const held = await owner.page.evaluate(() => ({
      connections: window.__opened.connections.length,
      liveTracks: window.__opened.tracks.filter((track) => track.readyState !== 'ended').length,
      sessions: document.querySelectorAll('[data-huddle-call]').length,
    }));
    expect(held).toEqual({ connections: 1, liveTracks: 1, sessions: 1 });

    await owner.page.goto('/app/threads');
    const left = await lastReport(owner.page, '/app');
    expect(left.openConnections).toBe(0);
    expect(left.liveTracks).toBe(0);
    expect(left.runningAudio).toBe(0);
    expect(left.openStreams).toEqual([]);
  } finally {
    await owner.context.close();
    await peer.context.close();
  }
});
