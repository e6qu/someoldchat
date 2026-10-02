#!/usr/bin/env node
// Writes internal/slackobject/zone_names.json: for every time zone ICU knows,
// the long name it carries at each UTC offset it uses, which is what Slack
// reports as a user's tz_label ("Pacific Daylight Time"). Go has no source for
// these names; ICU, which browsers use for the zone they report, does.
//
// The zones are ICU's and every name in Go's own zoneinfo archive, so an IANA
// alias Go accepts ("Asia/Kolkata" beside ICU's "Asia/Calcutta") has a name too.
//
// Each zone is sampled on the 1st and 15th of every month over three years, so
// both sides of every daylight-saving rule in force are seen. Run it with a
// Node.js built with full ICU (the default) after an ICU or tzdata update:
//
//	node scripts/update-zone-names.mjs
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const out = fileURLToPath(new URL('../internal/slackobject/zone_names.json', import.meta.url));
const years = [2025, 2026, 2027];

function part(zone, instant, style) {
  return new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: style })
    .formatToParts(instant).find((piece) => piece.type === 'timeZoneName').value;
}

function offsetSeconds(zone, instant) {
  const label = part(zone, instant, 'longOffset');
  if (label === 'GMT') return 0;
  const match = /^GMT([+-])(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(label);
  if (!match) throw new Error(`${zone}: unreadable offset ${label}`);
  const seconds = Number(match[2]) * 3600 + Number(match[3]) * 60 + Number(match[4] ?? 0);
  return match[1] === '-' ? -seconds : seconds;
}

// goZones lists the entries of $GOROOT/lib/time/zoneinfo.zip from the archive's
// central directory, which is all a listing needs.
function goZones() {
  const root = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
  const archive = readFileSync(`${root}/lib/time/zoneinfo.zip`);
  const end = archive.lastIndexOf(Buffer.from([0x50, 0x4b, 0x05, 0x06]));
  let at = archive.readUInt32LE(end + 16);
  const zones = [];
  for (let entry = archive.readUInt16LE(end + 10); entry > 0; entry--) {
    const nameLength = archive.readUInt16LE(at + 28);
    const extraLength = archive.readUInt16LE(at + 30);
    const commentLength = archive.readUInt16LE(at + 32);
    zones.push(archive.toString('utf8', at + 46, at + 46 + nameLength));
    at += 46 + nameLength + extraLength + commentLength;
  }
  return zones;
}

function known(zone) {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

const zones = [...new Set([...Intl.supportedValuesOf('timeZone'), 'UTC', ...goZones().filter(known)])].sort();
const names = {};
for (const zone of zones) {
  const byOffset = new Map();
  for (const year of years) {
    for (let month = 0; month < 12; month++) {
      for (const day of [1, 15]) {
        const instant = new Date(Date.UTC(year, month, day, 12));
        byOffset.set(offsetSeconds(zone, instant), part(zone, instant, 'long'));
      }
    }
  }
  names[zone] = [...byOffset.entries()].sort((a, b) => a[0] - b[0]);
}
writeFileSync(out, JSON.stringify(names, null, 0).replace(/\],"/g, '],\n"') + '\n');
console.log(`${Object.keys(names).length} zones written to ${out}`);
