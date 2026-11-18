#!/usr/bin/env node
/*
 * Auditable maintenance for projected execution rounds.
 *
 * This tool edits only the admin activity journal. Conversation memory,
 * message stores, task state, send ledgers, and completed rounds are outside
 * its scope. It is deliberately dry-run by default.
 */
'use strict';

const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const zlib = require('zlib');
const { project } = require('../internal/admin/static/rounds.js');

const ARCHIVE_SCOPE = crypto.createHash('sha256')
  .update('admin\0instance\0activity', 'utf8').digest('hex');
const STATUS_GROUPS = new Set(['pending', 'active']);
const SHANGHAI = new Intl.DateTimeFormat('en-CA', {
  timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit'
});

function fail(message) { throw new Error(message); }
function readJson(file) {
  const raw = fs.readFileSync(file);
  try { return { value: JSON.parse(raw.toString('utf8')), raw }; }
  catch { fail(`JSON 不可读：${file}`); }
}
function dateKey(value) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return '';
  return SHANGHAI.format(date);
}
function sha256(buffer) { return crypto.createHash('sha256').update(buffer).digest('hex'); }
function isSafeRegular(file) {
  const st = fs.lstatSync(file);
  return st.isFile() && !st.isSymbolicLink();
}
function assertOutside(child, parent, label) {
  const a = path.resolve(child).toLowerCase();
  const b = path.resolve(parent).toLowerCase();
  if (a === b || a.startsWith(b + path.sep)) fail(`${label} 不能位于数据目录内`);
}
function atomicWrite(file, bytes) {
  const dir = path.dirname(file);
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
  const temp = path.join(dir, `.round-cleanup-${process.pid}-${Date.now()}-${Math.random().toString(16).slice(2)}`);
  let fd;
  try {
    fd = fs.openSync(temp, 'wx', 0o600);
    fs.writeFileSync(fd, bytes);
    fs.fsyncSync(fd);
    fs.closeSync(fd); fd = undefined;
    fs.renameSync(temp, file);
  } finally {
    if (fd !== undefined) fs.closeSync(fd);
    try { fs.unlinkSync(temp); } catch {}
  }
}
function parseBatch(file, scope) {
  if (!isSafeRegular(file)) fail(`归档不是普通文件：${file}`);
  const compressed = fs.readFileSync(file);
  let raw;
  try { raw = zlib.gunzipSync(compressed); } catch { fail(`归档压缩内容无效：${file}`); }
  const expected = path.basename(file, '.json.gz');
  if (!/^[0-9a-f]{64}$/.test(expected) || sha256(raw) !== expected) fail(`归档摘要校验失败：${file}`);
  let batch;
  try { batch = JSON.parse(raw.toString('utf8')); } catch { fail(`归档 JSON 无效：${file}`); }
  if (batch.version !== 1 || batch.kind !== 'activity' || batch.scope !== scope || !Array.isArray(batch.data) || batch.data.length > 128) {
    fail(`归档结构不符合 activity 契约：${file}`);
  }
  return { file, compressed, raw, batch, entries: batch.data };
}
function archiveBytes(entries, scope) {
  const raw = Buffer.from(JSON.stringify({ version: 1, kind: 'activity', scope, data: entries }), 'utf8');
  const id = sha256(raw);
  return { id, raw, compressed: zlib.gzipSync(raw, { level: 6 }) };
}
function acquireLock(dataDir) {
  const file = path.join(dataDir, '.execution-round-cleanup.lock');
  try {
    const fd = fs.openSync(file, 'wx', 0o600);
    fs.writeFileSync(fd, `${process.pid}\n`);
    fs.closeSync(fd);
    return () => { try { fs.unlinkSync(file); } catch {} };
  } catch (error) {
    if (error.code === 'EEXIST') fail(`清理锁已存在：${file}。确认管理端已停止后再重试`);
    throw error;
  }
}

function loadSources(dataDir) {
  const activeFile = path.join(dataDir, 'activity.json');
  const activeJson = fs.existsSync(activeFile) ? readJson(activeFile) : { value: [], raw: Buffer.from('[]') };
  const active = activeJson.value;
  if (!Array.isArray(active) || active.length > 20000) fail('activity.json 必须是最多 20000 条记录的数组');
  const archiveDir = path.join(dataDir, 'archive', 'activity', ARCHIVE_SCOPE);
  const batches = [];
  if (fs.existsSync(archiveDir)) {
    const st = fs.lstatSync(archiveDir);
    if (!st.isDirectory() || st.isSymbolicLink()) fail(`归档目录不安全：${archiveDir}`);
    for (const name of fs.readdirSync(archiveDir).sort()) {
      if (!name.endsWith('.json.gz')) continue;
      batches.push(parseBatch(path.join(archiveDir, name), ARCHIVE_SCOPE));
    }
  }
  return { activeFile, active, activeRaw: activeJson.raw, archiveDir, batches };
}
function sourceSignature(sources) {
  return JSON.stringify({
    active: sha256(sources.activeRaw),
    archives: sources.batches.map(batch => ({ name: path.basename(batch.file), sha256: sha256(batch.compressed) }))
  });
}
function buildPlan(dataDir, date, expected) {
  const sources = loadSources(dataDir);
  const all = sources.active.concat(...sources.batches.map(batch => batch.entries));
  const rounds = project(all);
  const selected = rounds.filter(round => !round.system && STATUS_GROUPS.has(round.group) && dateKey(round.time) === date);
  const selectedEntries = new Set(selected.flatMap(round => round.entries));
  const activeRemove = sources.active.filter(entry => selectedEntries.has(entry)).length;
  const archiveRemove = sources.batches.reduce((sum, batch) => sum + batch.entries.filter(entry => selectedEntries.has(entry)).length, 0);
  const counts = { pending: selected.filter(round => round.group === 'pending').length, active: selected.filter(round => round.group === 'active').length };
  if (expected.pending !== undefined && counts.pending !== expected.pending) fail(`待处理回合数为 ${counts.pending}，与期望 ${expected.pending} 不符`);
  if (expected.active !== undefined && counts.active !== expected.active) fail(`进行中回合数为 ${counts.active}，与期望 ${expected.active} 不符`);
  if (activeRemove + archiveRemove === 0) fail(`日期 ${date} 没有可清理的待处理或进行中回合`);
  return { dataDir, date, sources, rounds, selected, selectedEntries, counts, activeRemove, archiveRemove };
}
function describePlan(plan) {
  return {
    date: plan.date,
    counts: plan.counts,
    entries: { active: plan.activeRemove, archived: plan.archiveRemove },
    rounds: plan.selected.map(round => ({ id: round.id, status: round.status, group: round.group, time: round.time, updated: round.updated, entry_count: round.entries.length }))
  };
}
function applyPlan(plan, quarantineDir) {
  const dataDir = path.resolve(plan.dataDir);
  assertOutside(quarantineDir, dataDir, '恢复目录');
  if (fs.existsSync(quarantineDir)) {
    const st = fs.lstatSync(quarantineDir);
    if (!st.isDirectory() || st.isSymbolicLink()) fail(`恢复目录不安全：${quarantineDir}`);
    if (fs.readdirSync(quarantineDir).length) fail(`恢复目录必须为空：${quarantineDir}`);
  } else fs.mkdirSync(quarantineDir, { recursive: true, mode: 0o700 });
  const unlock = acquireLock(dataDir);
  try {
    // Re-read after obtaining the lock so a plan made before a concurrent
    // writer cannot silently overwrite newer activity.
    const current = loadSources(dataDir);
    if (sourceSignature(current) !== sourceSignature(plan.sources)) fail('活动日志或归档在计划后发生变化，已拒绝覆盖');
    const currentPlan = buildPlan(dataDir, plan.date, plan.counts);
    const originalFiles = [];
    if (currentPlan.activeRemove) originalFiles.push({ source: currentPlan.sources.activeFile, relative: 'activity.json', bytes: fs.readFileSync(currentPlan.sources.activeFile) });
    for (const batch of currentPlan.sources.batches) {
      if (batch.entries.some(entry => currentPlan.selectedEntries.has(entry))) {
        originalFiles.push({ source: batch.file, relative: path.join('archive', 'activity', ARCHIVE_SCOPE, path.basename(batch.file)), bytes: batch.compressed });
      }
    }
    for (const item of originalFiles) {
      const destination = path.join(quarantineDir, item.relative);
      fs.mkdirSync(path.dirname(destination), { recursive: true, mode: 0o700 });
      fs.writeFileSync(destination, item.bytes, { flag: 'wx', mode: 0o600 });
    }
    const manifest = { schema: 1, created_at: new Date().toISOString(), tool: 'cleanup_execution_rounds.cjs', scope: ARCHIVE_SCOPE, plan: describePlan(currentPlan), originals: originalFiles.map(item => ({ path: item.relative, sha256: sha256(item.bytes), bytes: item.bytes.length })) };
    atomicWrite(path.join(quarantineDir, 'manifest.json'), Buffer.from(JSON.stringify(manifest, null, 2) + '\n', 'utf8'));
    const archiveChanges = currentPlan.sources.batches.map(batch => {
      const kept = batch.entries.filter(entry => !currentPlan.selectedEntries.has(entry));
      if (kept.length === batch.entries.length) return null;
      const next = kept.length ? archiveBytes(kept, ARCHIVE_SCOPE) : null;
      return { oldPath: batch.file, oldBytes: batch.compressed, newPath: next && path.join(currentPlan.sources.archiveDir, `${next.id}.json.gz`), newBytes: next && next.compressed };
    }).filter(Boolean);
    const generated = [];
    try {
      if (currentPlan.activeRemove) {
        const kept = currentPlan.sources.active.filter(entry => !currentPlan.selectedEntries.has(entry));
        atomicWrite(currentPlan.sources.activeFile, Buffer.from(JSON.stringify(kept), 'utf8'));
      }
      for (const change of archiveChanges) {
        if (change.newPath) {
          atomicWrite(change.newPath, change.newBytes);
          generated.push(change.newPath);
        }
        fs.unlinkSync(change.oldPath);
      }
    } catch (error) {
      // Restore every changed source from the quarantine copy before surfacing
      // the failure. The manifest and raw originals remain available even if
      // the filesystem refuses a rollback operation.
      try {
        if (currentPlan.activeRemove) atomicWrite(currentPlan.sources.activeFile, fs.readFileSync(path.join(quarantineDir, 'activity.json')));
        for (const change of archiveChanges) {
          for (const file of generated) if (file === change.newPath) { try { fs.unlinkSync(file); } catch {} }
          atomicWrite(change.oldPath, change.oldBytes);
        }
      } catch (rollbackError) {
        error.message += `；自动回滚失败：${rollbackError.message}；请使用 ${quarantineDir} 恢复`;
      }
      throw error;
    }
    return { ...describePlan(currentPlan), quarantine: quarantineDir, manifest: path.join(quarantineDir, 'manifest.json') };
  } finally { unlock(); }
}

function usage(code = 2) {
  console.error('用法：node scripts/cleanup_execution_rounds.cjs --data-dir <管理端私有目录> --date YYYY-MM-DD [--expect-pending N --expect-active N] [--apply --quarantine E:\\待删除\\... ]');
  process.exit(code);
}
function parseArgs(argv) {
  const out = { expected: {} };
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = () => { if (i + 1 >= argv.length) usage(); return argv[++i]; };
    if (arg === '--data-dir') out.dataDir = next();
    else if (arg === '--date') out.date = next();
    else if (arg === '--expect-pending') out.expected.pending = Number(next());
    else if (arg === '--expect-active') out.expected.active = Number(next());
    else if (arg === '--quarantine') out.quarantine = next();
    else if (arg === '--apply') out.apply = true;
    else if (arg === '--help' || arg === '-h') usage(0);
    else usage();
  }
  if (!out.dataDir || !/^\d{4}-\d{2}-\d{2}$/.test(out.date || '') || Object.values(out.expected).some(n => !Number.isInteger(n) || n < 0)) usage();
  return out;
}
function main(argv = process.argv.slice(2)) {
  const args = parseArgs(argv);
  if (args.apply && (args.expected.pending === undefined || args.expected.active === undefined)) {
    throw new Error('--apply 必须同时指定 --expect-pending 和 --expect-active');
  }
  const plan = buildPlan(path.resolve(args.dataDir), args.date, args.expected);
  if (!args.apply) {
    console.log(JSON.stringify({ mode: 'dry-run', ...describePlan(plan), apply_hint: '复核数量后追加 --apply，并指定空的 --quarantine 目录' }, null, 2));
    return;
  }
  const quarantine = path.resolve(args.quarantine || `E:\\待删除\\execution-round-cleanup-${Date.now()}`);
  console.log(JSON.stringify({ mode: 'apply', ...applyPlan(plan, quarantine) }, null, 2));
}

if (require.main === module) {
  try { main(); } catch (error) { console.error(`清理未执行：${error.message}`); process.exitCode = 1; }
}
module.exports = { ARCHIVE_SCOPE, buildPlan, applyPlan, describePlan, archiveBytes, dateKey };
