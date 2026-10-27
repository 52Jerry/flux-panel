import importlib.util, json, pathlib, types, unittest
SOURCE = pathlib.Path(__file__).with_name('receive-scaling.py')
spec = importlib.util.spec_from_file_location('receive_scaling', SOURCE)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class ReceiveScalingTest(unittest.TestCase):

    def setUp(self):
        self.real_pathlib = module.pathlib
        test = self
        self.original = {p: '32768' if p.endswith('rps_flow_cnt') else '0' for p in module.ALLOWED}
        targets = {p: '0' if '/ens19/' in p else 'ff' if p.endswith('rps_cpus') else '65536' for p in module.ALLOWED}
        self.config = {'online': '0-7', 'boot_id': 'boot-one', 'settings': {p: {'original': self.original[p], 'target': targets[p]} for p in module.ALLOWED}}
        self.fs = {**self.original, '/manifest.json': json.dumps(self.config), '/sys/devices/system/cpu/online': '0-7', '/proc/sys/kernel/random/boot_id': 'boot-one'}
        self.writes = []
        self.fail = None

        class VirtualPath:

            def __init__(self, p):
                self.p = str(p)

            def __str__(self):
                return self.p

            @property
            def name(self):
                return self.p.rsplit('/', 1)[-1]

            def __truediv__(self, p):
                return VirtualPath(self.p + '/' + p)

            def with_name(self, n):
                return VirtualPath(self.p.rsplit('/', 1)[0] + '/' + n)

            def read_text(self):
                return test.fs[self.p]

            def write_text(self, v):
                if test.fail == self.p:
                    test.fail = None
                    raise OSError('injected write failure')
                test.writes.append(self.p)
                test.fs[self.p] = v

            def glob(self, pattern):
                return [object()]

            def chmod(self, mode):
                pass

            def replace(self, target):
                test.fs[str(target)] = test.fs.pop(self.p)
        module.pathlib = types.SimpleNamespace(Path=VirtualPath)

    def tearDown(self):
        module.pathlib = self.real_pathlib

    def test_apply_and_restore(self):
        self.assertTrue(module.manage('apply', '/manifest.json')['applied'])
        module.manage('rollback', '/manifest.json')
        for p, v in self.original.items():
            self.assertEqual(module.number(p, self.fs[p]), module.number(p, v))

    def test_external_change_refused_before_any_write(self):
        self.fs['/proc/sys/net/core/rps_sock_flow_entries'] = '123'
        with self.assertRaisesRegex(RuntimeError, 'changed externally'):
            module.manage('apply', '/manifest.json')
        self.assertEqual(self.writes, [])

    def test_boot_reset_accepted_only_once_per_boot(self):
        self.fs['/proc/sys/kernel/random/boot_id'] = 'boot-two'
        for p in self.original:
            self.fs[p] = '0'
        self.assertTrue(module.manage('apply', '/manifest.json')['applied'])
        self.assertEqual(json.loads(self.fs['/manifest.json'])['boot_id'], 'boot-two')
        self.fs['/sys/class/net/ens18/queues/rx-0/rps_flow_cnt'] = '0'
        with self.assertRaisesRegex(RuntimeError, 'changed externally'):
            module.manage('apply', '/manifest.json')

    def test_partial_failure_restores_prior_values(self):
        self.fail = '/proc/sys/net/core/rps_sock_flow_entries'
        with self.assertRaisesRegex(OSError, 'injected'):
            module.manage('apply', '/manifest.json')
        for p, v in self.original.items():
            self.assertEqual(module.number(p, self.fs[p]), module.number(p, v))

    def test_topology_change_refused(self):
        self.fs['/sys/devices/system/cpu/online'] = '0-15'
        with self.assertRaisesRegex(RuntimeError, 'topology'):
            module.manage('apply', '/manifest.json')
        self.assertEqual(self.writes, [])
if __name__ == '__main__':
    unittest.main()
