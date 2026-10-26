import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/dashboard-flow.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020 },
}).outputText;
const { dashboardFlowData } = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
const at = new Date(2026, 9, 27, 0, 20).getTime();
const hour = new Date(2026, 9, 27, 0).getTime();
const record = (extra = {}) => ({ id: 10, flow: 100, totalFlow: 1000, time: '00:00', createdTime: hour + 1, ...extra });

test('hour-end snapshot goes to previous interval and live delta updates current hour', () => {
  const data = dashboardFlowData([record()], 1040, at);
  assert.equal(data.length, 24);
  assert.equal(data.at(-2).time, '23:00');
  assert.equal(data.at(-2).flow, 100);
  assert.equal(data.at(-1).flow, 40);
  assert.equal(data.at(-1).isCurrent, true);
});
test('padding and duplicate rows cannot erase genuine measurements', () => {
  const data = dashboardFlowData([record(), record({ id: 9, flow: 3 }), { time: '00:00', flow: 0, totalFlow: 0 }], 1040, at);
  assert.equal(data.at(-2).flow, 100);
  assert.equal(data.at(-1).flow, 40);
});
test('stale dates and future records cannot wrap into a current hour', () => {
  const data = dashboardFlowData([record({ createdTime: hour - 48 * 3600000 }), record({ createdTime: hour + 3600000 })], 1040, at);
  assert.ok(data.every(row => row.flow === null));
});
test('missing latest snapshot leaves a gap instead of inventing hourly traffic', () => {
  const data = dashboardFlowData([record({ createdTime: hour - 3600000 })], 1040, at);
  assert.equal(data.at(-1).flow, null);
});
test('counter reset cannot produce negative current traffic', () => {
  assert.equal(dashboardFlowData([record()], 5, at).at(-1).flow, 5);
});
test('legacy real records without timestamps still work across midnight', () => {
  const data = dashboardFlowData([record({ createdTime: undefined })], 1040, at);
  assert.equal(data.at(-1).flow, 40);
  assert.equal(data.at(-2).flow, 100);
});
