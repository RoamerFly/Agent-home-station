#!/usr/bin/env node
'use strict';

const assert = require('assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const zlib = require('zlib');
const crypto = require('crypto');
const { buildPlan, applyPlan, ARCHIVE_SCOPE, archiveBytes } = require('./cleanup_execution_rounds.cjs');

function entry(time, values) {
  return { id: values.id || `id-${Math.random()}`, time, thread_id: values.thread_id || 'thread', message_id: values.message_id || '', event: values.event || 'message_received', category: values.category || 'received', status: values.status || 'received', content: values.content || '' };
}
function makeStore(active, archived) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'execution-round-cleanup-'));
  const archiveDir = path.join(root, 'archive', 'activity', ARCHIVE_SCOPE);
  fs.mkdirSync(archiveDir, { recursive: true });
  fs.writeFileSync(path.join(root, 'activity.json'), JSON.stringify(active));
  for (const entries of archived) {
    const batch = archiveBytes(entries, ARCHIVE_SCOPE);
    fs.writeFileSync(path.join(archiveDir, `${batch.id}.json.gz`), batch.compressed);
  }
  return root;
}
function testPlanUsesProjectedStatusAndDate() {
  const day = '2026-11-18';
  const pending = entry('2026-11-18T01:00:00Z', { id: 'pending', message_id: 'pending', status: 'received' });
  const active = entry('2026-11-18T02:00:00Z', { id: 'active', message_id: 'active', event: 'message_processing', category: 'decision', status: 'running' });
  const completed = entry('2026-11-18T03:00:00Z', { id: 'done', message_id: 'done', event: 'message_processed', category: 'decision', status: 'completed' });
  const archivedPending = entry('2026-11-18T04:00:00Z', { id: 'archived-pending', message_id: 'archived-pending', status: 'queued' });
  const archivedOld = entry('2026-11-17T04:00:00Z', { id: 'old', message_id: 'old', status: 'queued' });
  const root = makeStore([pending, active, completed], [[archivedPending, archivedOld]]);
  const plan = buildPlan(root, day, { pending: 2, active: 1 });
  assert.deepStrictEqual(plan.counts, { pending: 2, active: 1 });
  assert.strictEqual(plan.selected.length, 3);
  assert(plan.selected.every(round => ['pending', 'active'].includes(round.group)));
  assert.strictEqual(plan.selected.some(round => round.id.includes('done')), false, 'completed round selected');
}
function testApplyQuarantinesRawFilesAndKeepsCompleted() {
  const pending = entry('2026-11-18T01:00:00Z', { id: 'pending', message_id: 'pending', status: 'received' });
  const active = entry('2026-11-18T02:00:00Z', { id: 'active', message_id: 'active', event: 'message_processing', category: 'decision', status: 'running' });
  const completed = entry('2026-11-18T03:00:00Z', { id: 'done', message_id: 'done', event: 'message_processed', category: 'decision', status: 'completed' });
  const archivedPending = entry('2026-11-18T04:00:00Z', { id: 'archived-pending', message_id: 'archived-pending', status: 'queued' });
  const archivedDone = entry('2026-11-18T05:00:00Z', { id: 'archived-done', message_id: 'archived-done', event: 'message_processed', category: 'decision', status: 'completed' });
  const root = makeStore([pending, active, completed], [[archivedPending, archivedDone]]);
  const sourceActivity = fs.readFileSync(path.join(root, 'activity.json'));
  const sourceArchive = fs.readdirSync(path.join(root, 'archive', 'activity', ARCHIVE_SCOPE))[0];
  const sourceArchiveBytes = fs.readFileSync(path.join(root, 'archive', 'activity', ARCHIVE_SCOPE, sourceArchive));
  const quarantine = path.join(os.tmpdir(), `execution-round-quarantine-${Date.now()}-${Math.random().toString(16).slice(2)}`);
  const plan = buildPlan(root, '2026-11-18', { pending: 2, active: 1 });
  const result = applyPlan(plan, quarantine);
  assert.strictEqual(result.counts.pending, 2);
  assert.strictEqual(JSON.stringify(JSON.parse(fs.readFileSync(path.join(root, 'activity.json')))).includes('pending'), false);
  assert.strictEqual(JSON.stringify(JSON.parse(fs.readFileSync(path.join(root, 'activity.json')))).includes('done'), true);
  const remainingArchiveFiles = fs.readdirSync(path.join(root, 'archive', 'activity', ARCHIVE_SCOPE)).filter(name => name.endsWith('.json.gz'));
  assert.strictEqual(remainingArchiveFiles.length, 1);
  const kept = JSON.parse(zlib.gunzipSync(fs.readFileSync(path.join(root, 'archive', 'activity', ARCHIVE_SCOPE, remainingArchiveFiles[0])))).data;
  assert(kept.some(item => item.id === 'archived-done'));
  assert(!kept.some(item => item.id === 'archived-pending'));
  assert.deepStrictEqual(fs.readFileSync(path.join(quarantine, 'activity.json')), sourceActivity);
  assert.deepStrictEqual(fs.readFileSync(path.join(quarantine, 'archive', 'activity', ARCHIVE_SCOPE, sourceArchive)), sourceArchiveBytes);
  const manifest = JSON.parse(fs.readFileSync(path.join(quarantine, 'manifest.json')));
  assert.strictEqual(manifest.plan.counts.active, 1);
  assert.strictEqual(manifest.originals.length, 2);
}
function testExpectationMismatchDoesNotWrite() {
  const pending = entry('2026-11-18T01:00:00Z', { id: 'pending', message_id: 'pending', status: 'received' });
  const root = makeStore([pending], []);
  assert.throws(() => buildPlan(root, '2026-11-18', { pending: 32, active: 5 }), /待处理回合数为 1/);
  assert.strictEqual(JSON.parse(fs.readFileSync(path.join(root, 'activity.json'))).length, 1);
}

testPlanUsesProjectedStatusAndDate();
testApplyQuarantinesRawFilesAndKeepsCompleted();
testExpectationMismatchDoesNotWrite();
console.log('cleanup execution rounds tests passed');
