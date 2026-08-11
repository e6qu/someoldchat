# Browser probes

Standalone reproductions for browser behaviour that the gated suite records but
cannot resolve. They live outside `specs/`, so `make browser-qualification`
never runs them, and each is invoked by hand against a fixture it starts itself.

A probe is not a gate. It exists to narrow where a recorded crash or flake lives,
and its result — including a negative one — is written into the product gap
audit rather than into a pass/fail count.

## webkit-app-teardown.probe.mjs

The gap audit records a WebKit crash that failed twice on CI, both times at
`page.goto` of a permalink, with the navigation's own request never completing —
so the crash is on leaving `/app` rather than on the page being opened.

This opens the deep-linked `/app` a permalink resolves to and leaves it, sixty
times, with nothing else happening, and asserts every load actually rendered a
timeline so a probe that navigated but drew nothing cannot pass green.

Run it:

    cd tests/browser
    npx playwright test --config=probes/playwright.probe.config.mjs

Local WebKit does not crash under it. That is a negative result: it does not
clear CI WebKit, a different build on a different OS under accumulated state, but
it removes the teardown-alone explanation. Reach for this probe when the crash
recurs.
