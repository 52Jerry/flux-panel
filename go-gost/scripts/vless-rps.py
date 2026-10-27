#!/usr/bin/env python3
"""Manage the SZ public-NIC RPS setting without restarting forwarding.

This deployment is intentionally limited to the verified 8-CPU/single-queue
host. It does not configure IRQ affinity, WG transport, TCP buffers or BBR.
"""
import json
import pathlib
import sys

QUEUE = pathlib.Path('/sys/class/net/ens18/queues/rx-0/rps_cpus')
BACKUP = pathlib.Path('/root/backups/flux-rps-20261027/before.json')
MASK = 0xff


def value(text):
    return int(text.strip().replace(',', ''), 16)


def main(action):
    original = json.loads(BACKUP.read_text())[str(QUEUE)]
    current = QUEUE.read_text().strip()
    if action == 'status':
        print(json.dumps({'queue': str(QUEUE), 'current': current,
                          'original': original, 'enabled': value(current) == MASK}))
        return
    if action not in ['apply', 'rollback']:
        raise ValueError('use apply, rollback or status')
    # Refuse to overwrite another operator's setting, including at shutdown.
    if value(current) not in [value(original), MASK]:
        raise RuntimeError('RPS changed externally; inspect before applying')
    if action == 'apply':
        online = pathlib.Path('/sys/devices/system/cpu/online').read_text().strip()
        queues = list(QUEUE.parent.parent.glob('rx-*'))
        if online != '0-7' or len(queues) != 1:
            raise RuntimeError('CPU/NIC topology changed; re-evaluate RPS')
        target = 'ff'
    else:
        target = original
    QUEUE.write_text(target + '\n')
    if value(QUEUE.read_text()) != value(target):
        QUEUE.write_text(current + '\n')
        raise RuntimeError('RPS verification failed; restored prior setting')
    print(json.dumps({'action': action, 'mask': QUEUE.read_text().strip()}))


if __name__ == '__main__':
    main(sys.argv[1] if len(sys.argv) == 2 else 'status')
