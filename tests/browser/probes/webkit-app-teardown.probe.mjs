import { test, expect } from '@playwright/test';

// A focused reproduction for the crash recorded in the product gap audit:
// WebKit failed twice on CI at page.goto of a permalink, both times with the
// navigation's own request never completing, so the crash is on leaving /app
// rather than on the page being opened. /app is the heaviest page in the suite —
// WebRTC, media and notification code — and this opens and leaves it many times
// with nothing else happening, to see whether the teardown alone is enough.
//
// It is a PROBE, not a gate: it lives outside specs/ so make browser-qualification
// never runs it, and it is invoked by hand against the running fixture. Whatever
// it shows is recorded in the gap audit, whichever way it goes.

const SESSION = 'browser-session';
const API_TOKEN = 'xoxb-browser';
const ITERATIONS = Number(process.env.PROBE_ITERATIONS || 60);

test('opening and leaving /app repeatedly does not crash the browser', async ({ page, context }) => {
  test.setTimeout(ITERATIONS * 4000 + 30000);
  await context.addCookies([{ name: 'sameoldchat_session', value: SESSION, url: 'http://127.0.0.1:18082' }]);

  // A real permalink, resolved the way the failing test resolved it: the crash
  // was navigating to one, which loads /app deep-linked to a message and scrolls
  // to it. A plain /app load is not the same navigation.
  const posted = await page.request.post('/api/chat.postMessage', {
    headers: { authorization: `Bearer ${API_TOKEN}`, 'content-type': 'application/json' },
    data: { channel: 'Cdev', text: `probe target ${Date.now()}` },
  });
  expect((await posted.json()).ok).toBe(true);
  await page.goto('/app');
  const permalink = await page.locator('a.copy-link').last().getAttribute('href');
  expect(permalink, 'the fixture produced no permalink').toMatch(/^\/archives\/Cdev\/p\d+$/);

  let crashed = null;
  page.on('crash', () => { crashed = 'page emitted crash'; });

  let confirmedLoads = 0;
  for (let i = 0; i < ITERATIONS && !crashed; i += 1) {
    // Into the deep-linked /app, and proven to have really rendered its
    // timeline rather than a fast redirect: the count is asserted at the end so
    // a probe that loaded nothing cannot pass green.
    const openStart = Date.now();
    const openResponse = await page.goto(permalink, { waitUntil: 'load' }).catch((error) => error);
    if (openResponse instanceof Error) { crashed = `open #${i}: ${openResponse.message}`; break; }
    const rendered = await page.locator('#timeline .message').first().isVisible().catch(() => false);
    if (rendered) { confirmedLoads += 1; }
    if (i < 3) { console.log(`PROBE iter ${i}: open ${Date.now() - openStart}ms, status ${openResponse && openResponse.status ? openResponse.status() : 'n/a'}, url ${page.url()}, rendered ${rendered}`); }

    // Away from it — a static page carrying none of the media code. This is the
    // teardown the crash traces pointed at.
    const leaveResponse = await page.goto('/signed-out', { waitUntil: 'load' }).catch((error) => error);
    if (leaveResponse instanceof Error) { crashed = `leave #${i}: ${leaveResponse.message}`; break; }
  }

  console.log(`PROBE: ${ITERATIONS} iterations, ${confirmedLoads} confirmed timeline renders, permalink ${permalink}`);
  expect(crashed, `crashed opening/leaving the deep-linked /app: ${crashed}`).toBeNull();
  // The probe proves nothing if it never actually loaded the heavy page. Most
  // iterations must have rendered a real timeline.
  expect(confirmedLoads, 'the probe navigated but rendered no timeline').toBeGreaterThan(ITERATIONS / 2);
});
