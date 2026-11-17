#!/usr/bin/env node
// Confirm the requested release tag, changelog and embedded UI notes agree.
// Usage: node scripts/check-release-metadata.mjs [vX.Y.Z[-prerelease]] [source-directory]
import { readFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(process.argv[3] || join(fileURLToPath(new URL('.', import.meta.url)), '..'));
const requested = process.argv[2];
const versionPattern = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$/;
const fail = message => { console.error(`Release metadata: ${message}`); process.exit(1); };
if (requested && !versionPattern.test(requested)) fail(`invalid tag ${requested}`);
const changelog = readFileSync(join(root, 'CHANGELOG.md'), 'utf8');
const headings = [...changelog.matchAll(/^## \[([^\]]+)\] - (\d{4}-\d{2}-\d{2})$/gm)];
if (headings.length === 0) fail('no dated version heading in CHANGELOG.md');
const [, latestVersion, latestDate] = headings[0];
if (!versionPattern.test(`v${latestVersion}`)) fail(`invalid latest changelog version ${latestVersion}`);
if (requested && requested !== `v${latestVersion}`) fail(`requested ${requested} but changelog newest is v${latestVersion}`);
const lines = changelog.split(/\r?\n/);
const headingLine = lines.findIndex(line => line === headings[0][0]);
const nextHeading = lines.findIndex((line, index) => index > headingLine && line.startsWith('## '));
const releaseBody = lines.slice(headingLine + 1, nextHeading < 0 ? undefined : nextHeading).join('\n').trim();
if (releaseBody.length < 30) fail(`release ${latestVersion} has no useful changelog body`);
let notes;
try { notes = JSON.parse(readFileSync(join(root, 'internal/web/releasenotes.json'), 'utf8')).releases; }
catch (err) { fail(`cannot read embedded release notes: ${err.message}`); }
if (!Array.isArray(notes) || !notes.length) fail('no embedded release notes');
if (notes[0].version !== latestVersion) fail(`embedded notes are for ${notes[0].version}; expected ${latestVersion}`);
if (notes[0].releasedOn !== latestDate) fail(`embedded notes date ${notes[0].releasedOn} differs from changelog ${latestDate}`);
if (!Array.isArray(notes[0].items) || notes[0].items.length === 0 || notes[0].items.some(item => typeof item !== 'string' || !item.trim())) fail('empty release note items');
const date = new Date(`${latestDate}T00:00:00Z`);
if (Number.isNaN(date.valueOf()) || date.toISOString().slice(0, 10) !== latestDate) fail(`invalid release date ${latestDate}`);
console.log(`Release metadata verified: v${latestVersion}, ${latestDate}, ${notes[0].items.length} user-facing changes`);
