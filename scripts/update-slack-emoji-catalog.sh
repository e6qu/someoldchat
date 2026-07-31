#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
revision=097705020bcf82331c9ef10df3425aad15f5043c
catalog_sha256=1d602e65be88772bf8cc368ce16b855d719eeddbafe128d471b80203f494d29f
license_sha256=ee9953a79bf2132b59b1342b217f1c377b3d03d9e7713006f6c3b89eb159f1db
work=$(mktemp -d "${TMPDIR:-/tmp}/sameoldchat-emoji-data.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM

curl -fsSL \
	"https://raw.githubusercontent.com/iamcal/emoji-data/$revision/emoji.json" \
	-o "$work/emoji.json"
curl -fsSL \
	"https://raw.githubusercontent.com/iamcal/emoji-data/$revision/LICENSE" \
	-o "$work/LICENSE"

printf '%s  %s\n' "$catalog_sha256" "$work/emoji.json" | shasum -a 256 -c -
printf '%s  %s\n' "$license_sha256" "$work/LICENSE" | shasum -a 256 -c -

jq -c '[.[] | .short_name as $primary | {
	n: $primary,
	d: .name,
	u: .unified,
	c: .category,
	a: ((.short_names // []) | map(select(. != $primary))),
	o: .sort_order,
	s: ((.skin_variations // {}) | length > 0)
}]' "$work/emoji.json" >"$work/catalog.json"

mkdir -p "$root/internal/slackemoji"
mv "$work/catalog.json" "$root/internal/slackemoji/catalog.json"
cp "$work/LICENSE" "$root/internal/slackemoji/emoji-data.LICENSE"
