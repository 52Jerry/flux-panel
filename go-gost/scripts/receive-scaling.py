#!/usr/bin/env python3
"""Apply or restore a reviewed, host-specific single-queue RPS/RFS manifest."""
import json
import pathlib
import sys

ALLOWED = {
    '/proc/sys/net/core/rps_sock_flow_entries',
    '/sys/class/net/ens18/queues/rx-0/rps_cpus',
    '/sys/class/net/ens18/queues/rx-0/rps_flow_cnt',
    '/sys/class/net/ens19/queues/rx-0/rps_flow_cnt',
}


def number(path, text):
    return int(text.strip().replace(',', ''), 16 if path.endswith('rps_cpus') else 10)


def manage(action, config):
    if action not in ['apply', 'rollback', 'status']:
        raise ValueError('expected apply, rollback or status')
    spec = json.loads(pathlib.Path(config).read_text())
    rows = spec['settings']
    if set(rows) != ALLOWED:
        raise ValueError('manifest must contain exactly the reviewed settings')
    current = {p: pathlib.Path(p).read_text().strip() for p in rows}
    current_boot = pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    boot_changed = bool(spec.get('boot_id')) and spec['boot_id'] != current_boot
    if action == 'status':
        return {'settings': current, 'applied': all(
            number(p, current[p]) == number(p, row['target']) for p, row in rows.items())}
    if action == 'apply':
        if pathlib.Path('/sys/devices/system/cpu/online').read_text().strip() != spec['online']:
            raise RuntimeError('CPU topology changed; review before applying')
        for nic in ['ens18', 'ens19']:
            if len(list((pathlib.Path('/sys/class/net') / nic / 'queues').glob('rx-*'))) != 1:
                raise RuntimeError('NIC queue topology changed; review before applying')
    # Validate every setting before changing any. Never undo another operator.
    for p, row in rows.items():
        known = number(p, current[p]) in [number(p, row['original']), number(p, row['target'])]
        # sysfs queue settings reset to zero at reboot. Accept that reset only
        # on a different boot; a live operator's disable is still protected.
        boot_reset = action == 'apply' and boot_changed and number(p, current[p]) == 0
        if not known and not boot_reset:
            raise RuntimeError('setting changed externally: ' + p)
    modified = []
    try:
        # Disable the previously dormant inner-NIC RFS table before the global
        # table is enabled. This keeps this deployment on the public NIC only.
        order = sorted(rows, key=lambda p: (not p.startswith('/sys/class/net/ens19/'), p.startswith('/proc/')))
        if action == 'rollback':
            order.reverse()
        for p in order:
            target = rows[p]['target' if action == 'apply' else 'original']
            pathlib.Path(p).write_text(target + '\n')
            modified.append(p)
            if number(p, pathlib.Path(p).read_text()) != number(p, target):
                raise RuntimeError('readback mismatch: ' + p)
        if action == 'apply' and boot_changed:
            # A zero setting later in this same boot may be an operator's
            # deliberate disable. Do not keep treating it as a boot reset.
            spec['boot_id'] = current_boot
            config_path = pathlib.Path(config)
            staged = config_path.with_name(config_path.name + '.boot-update')
            staged.write_text(json.dumps(spec, indent=2))
            staged.chmod(0o600)
            staged.replace(config_path)
    except BaseException:
        for p in reversed(modified):
            pathlib.Path(p).write_text(current[p] + '\n')
        raise
    return {'action': action, **manage('status', config)}


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: receive-scaling.py apply|rollback|status MANIFEST')
    print(json.dumps(manage(sys.argv[1], sys.argv[2]), indent=2))
