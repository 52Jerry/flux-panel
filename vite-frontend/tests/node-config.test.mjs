import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import ts from 'typescript';

const source = await readFile(new URL('../src/utils/node-config.ts', import.meta.url), 'utf8');
const compiled = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ES2020 },
}).outputText;
const { emptyNodeForm, nodeToForm, nodePayload, nodePortLabel, validPorts } =
  await import(`data:text/javascript;base64,${Buffer.from(compiled).toString('base64')}`);

test('modern configuration survives editing without legacy fields', () => {
  const node = {
    id: 3, name: 'SZ-new', serverIp: 'proxy.tkmo.xin', port: '5001-6000,61001',
    interfaceName: 'ens19', tcpListenAddr: '0.0.0.0', udpListenAddr: '172.16.115.226',
    http: 0, tls: 1, socks: 0,
  };
  const form = nodeToForm(node);
  assert.equal(form.schema, 'modern');
  assert.deepEqual(nodePayload(form), node);
  assert.equal(nodePortLabel(node), node.port);
  assert.equal(form.portSta.toString(), '1000');
});

test('legacy configuration keeps IPs and start/end ports', () => {
  const node = {
    id: 1, name: 'entry', serverIp: 'example.com', ip: '1.2.3.4,example.com',
    portSta: 5001, portEnd: 20169, http: 1, tls: 0, socks: 0,
  };
  const form = nodeToForm(node);
  assert.equal(form.schema, 'legacy');
  assert.equal(form.ipString, '1.2.3.4\nexample.com');
  assert.deepEqual(nodePayload(form), node);
  assert.equal(nodePortLabel(node), '5001-20169');
});

test('missing or null values do not crash form rendering or enable blocking', () => {
  const form = nodeToForm({ id: 2, portSta: null, portEnd: null, name: null, serverIp: null });
  assert.equal(form.portSta.toString(), '1000');
  assert.equal(form.portEnd.toString(), '65535');
  assert.equal(form.name, '');
  assert.equal(form.serverIp, '');
  assert.deepEqual([form.http, form.tls, form.socks], [0, 0, 0]);
  assert.equal(nodePortLabel({ id: 2 }), '-');
  const modern = nodeToForm({ id: 3, port: null });
  assert.equal(modern.schema, 'modern');
  assert.equal(modern.port, '');
  assert.equal(validPorts(modern.port), false);
});

test('validates single ports and multiple ordered ranges', () => {
  for (const port of ['1', '65535', '5001-20169', '1, 80, 443, 61000-61999']) {
    assert.equal(validPorts(port), true, port);
  }
  for (const port of ['', '0', '65536', '9000-8000', '1,', '1,,2', '-1', '1.5', '80abc', '1-2-3']) {
    assert.equal(validPorts(port), false, port);
  }
});

test('creation excludes form-only fields and id, and normalizes whitespace', () => {
  const form = { ...emptyNodeForm('modern'), name: ' entry ', serverIp: ' example.com ', port: '80, 443, 5001 - 6000' };
  const payload = nodePayload(form);
  assert.equal(payload.port, '80,443,5001-6000');
  assert.equal(payload.name, 'entry');
  assert.equal(payload.serverIp, 'example.com');
  for (const key of ['id', 'schema', 'ipString', 'ip', 'portSta', 'portEnd']) {
    assert.equal(key in payload, false, key);
  }
});
