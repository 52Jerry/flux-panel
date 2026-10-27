#!/usr/bin/env python3
"""Manage the reviewed SZ public qdisc; preserve an existing matching FQ."""
import json
import subprocess
import sys


def run(args):
    p = subprocess.run(args, capture_output=True, text=True, timeout=15)
    if p.returncode:
        raise RuntimeError('tc failed: ' + p.stderr[:200])
    return p.stdout


def main(action):
    if action not in ['apply', 'rollback', 'status']:
        raise ValueError('expected apply, rollback or status')
    qdiscs = json.loads(run(['tc', '-j', 'qdisc', 'show', 'dev', 'ens18']))
    if len(qdiscs) != 1:
        raise RuntimeError('unexpected public NIC queue topology')
    q = qdiscs[0]
    options = q.get('options', {})
    owned = q['kind'] == 'fq' and all(options.get(k) == v for k, v in {
        'limit': 10000, 'flow_limit': 100, 'buckets': 16384}.items())
    if action == 'status':
        return {'owned_fq': owned, 'qdiscs': qdiscs}
    if not owned and q['kind'] != 'fq_codel':
        raise RuntimeError('qdisc changed externally; inspect before changing')
    if action == 'apply' and not owned:
        run(['tc', 'qdisc', 'replace', 'dev', 'ens18', 'root', 'fq',
             'limit', '10000', 'flow_limit', '100', 'buckets', '16384'])
    elif action == 'rollback' and owned:
        run(['tc', 'qdisc', 'replace', 'dev', 'ens18', 'root', 'fq_codel',
             'limit', '10240', 'flows', '1024', 'quantum', '1514',
             'target', '5ms', 'interval', '100ms', 'memory_limit', '33554432', 'ecn'])
    result = main('status')
    if action == 'apply' and not result['owned_fq']:
        raise RuntimeError('FQ readback did not match the requested configuration')
    return {'action': action, **result}


if __name__ == '__main__':
    print(json.dumps(main(sys.argv[1] if len(sys.argv) == 2 else 'status'), indent=2))
