import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import test from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

const require = createRequire(import.meta.url);
const source = await readFile(new URL('../src/utils/node-congestion.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020 },
}).outputText;
const helpers = await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);
const { parseNodeCongestion, formatCongestionValue, formatCongestionBytes, formatCongestionTime } = helpers;
const componentSource = await readFile(new URL('../src/components/node-congestion.tsx', import.meta.url), 'utf8');
const component = { exports: {} };
vm.runInNewContext(ts.transpileModule(componentSource, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText, {
  exports: component.exports,
  require: path => path === '@/utils/node-congestion' ? helpers : require(path),
});
const render = data => renderToStaticMarkup(React.createElement(component.exports.NodeCongestionDetails, { data }));

test('legacy, missing, or invalid congestion remains absent without crashing the detail', () => {
  for (const value of [undefined, null, false, 0, 'data', []]) {
    assert.equal(parseNodeCongestion(value), null);
    assert.match(render(parseNodeCongestion(value)), /暂无数据/);
  }
  assert.match(render(null), /旧版本节点可能不支持/);
});

test('missing, null and invalid metrics are never coerced to healthy zero', () => {
  const result = parseNodeCongestion({
    active_transfers: '4', bytes_forwarded_total: Number.MAX_SAFE_INTEGER + 1,
    active_stalled_writes: -1, write_timeouts_total: false,
    tcp_sampling_supported: true, tcp_sampled_connections: 1,
    tcp_rtt_ms_avg: NaN, tcp_retransmissions: null, tcp_notsent_bytes: Infinity,
  });
  for (const field of ['active_transfers', 'bytes_forwarded_total', 'active_stalled_writes',
    'write_timeouts_total', 'tcp_rtt_ms_avg', 'tcp_retransmissions', 'tcp_notsent_bytes']) {
    assert.equal(result[field], null, field);
  }
  assert.equal(formatCongestionValue(null), '暂无数据');
  assert.equal(formatCongestionBytes(undefined), '暂无数据');
  assert.equal(formatCongestionTime(1e20), '暂无数据');
});

test('user-space, unsupported and empty samples suppress all kernel values even if sent as zero', () => {
  for (const [supported, sampled] of [[false, 1], [null, 1], [true, 0], [true, undefined]]) {
    const result = parseNodeCongestion({
      active_transfers: 2, tcp_sampling_supported: supported, tcp_sampled_connections: sampled,
      tcp_rtt_ms_avg: 0, tcp_min_rtt_ms: 0, tcp_retransmissions: 0,
      tcp_notsent_bytes: 0, tcp_delivery_rate_bps: 0, tcp_bbr_connections: 0, tcp_cubic_connections: 0,
    });
    assert.equal(result.active_transfers, 2);
    for (const field of ['tcp_rtt_ms_avg', 'tcp_min_rtt_ms', 'tcp_retransmissions', 'tcp_notsent_bytes',
      'tcp_delivery_rate_bps', 'tcp_bbr_connections', 'tcp_cubic_connections']) {
      assert.equal(result[field], null, field);
    }
    const html = render(result);
    assert.match(html, /BBR 连接（样本）<\/dt><dd[^>]*>暂无数据<\/dd>/);
    assert.match(html, /CUBIC 连接（样本）<\/dt><dd[^>]*>暂无数据<\/dd>/);
  }
});

test('valid measured zeroes survive while partial samples retain nulls and sampling limits', () => {
  const result = parseNodeCongestion({
    observed_at: 1793000000000, active_transfers: 0, active_stalled_writes: 0,
    tcp_sampling_supported: true, tcp_sampling_limited: true,
    tcp_tracked_connections: 512, tcp_sampled_connections: 64,
    tcp_rtt_ms_avg: 12.5, tcp_retransmissions: 0, tcp_notsent_bytes: 2048,
    tcp_bbr_connections: 24, tcp_cubic_connections: 0,
    tcp_min_rtt_ms: null, future_metric: 42,
  });
  assert.equal(result.tcp_retransmissions, 0);
  assert.equal(result.tcp_min_rtt_ms, null);
  assert.equal('future_metric' in result, false);
  const html = render(result);
  assert.match(html, /12.5 ms/);
  assert.match(html, /2 KiB/);
  assert.match(html, /64 \/ 512/);
  assert.match(html, /已达采样数量限制/);
  assert.match(html, /并非丢包率/);
  assert.match(html, /不代表对端已收到/);
  assert.match(html, /BBR 连接（样本）<\/dt><dd[^>]*>24<\/dd>/);
  assert.match(html, /CUBIC 连接（样本）<\/dt><dd[^>]*>0<\/dd>/);
  assert.match(html, /不表示整条链路已启用 BBR/);
  assert.equal(formatCongestionValue(0), '0');
  assert.equal(formatCongestionBytes(0), '0 B');
});

test('a later legacy report clears previous congestion instead of retaining stale measurements', () => {
  let congestion = parseNodeCongestion({ active_transfers: 3 });
  assert.equal(congestion.active_transfers, 3);
  congestion = parseNodeCongestion(JSON.parse('{"memory_usage":1}').congestion);
  assert.equal(congestion, null);
});
