#!/usr/bin/env python3
"""Prints the article text of saved Slack reference pages.

The external contract gate saves the reference page of every method and event
Slack adds. A workspace without network access to docs.slack.dev reads them
from the CI log, so each page is reduced to its article: headings, paragraphs,
list items and table rows on lines of their own, without navigation.
"""

import html
import pathlib
import re
import sys

BLOCK = re.compile(r"</?(h[1-6]|p|li|tr|pre|div|section|table|ul|ol|br)\b[^>]*>", re.I)
CELL = re.compile(r"</t[dh]>", re.I)


def article(source: str) -> str:
    match = re.search(r"<article\b.*?</article>", source, re.S | re.I) or re.search(r"<main\b.*?</main>", source, re.S | re.I)
    body = match.group(0) if match else source
    body = re.sub(r"<(script|style|nav|svg)\b.*?</\1>", " ", body, flags=re.S | re.I)
    body = CELL.sub(" | ", body)
    body = BLOCK.sub("\n", body)
    body = html.unescape(re.sub(r"<[^>]+>", "", body))
    lines = (re.sub(r"[ \t ]+", " ", line).strip() for line in body.splitlines())
    return "\n".join(line for line in lines if line)


def main(root: str) -> None:
    for page in sorted(pathlib.Path(root).rglob("*.html")):
        print(f"===== {page.relative_to(root)}")
        print(article(page.read_text(encoding="utf-8", errors="replace")))


if __name__ == "__main__":
    main(sys.argv[1])
