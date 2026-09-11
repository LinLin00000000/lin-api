"""Regression checks for the fail-closed documentation classifier."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('runtime_changed.py').resolve()
WORKFLOW = SCRIPT.parents[1] / 'workflows' / 'ci.yml'


class ChangesTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.git('init', '-q')
        self.git('config', 'user.name', 'fixture')
        self.git('config', 'user.email', 'fixture@example.invalid')
        (self.root / 'runtime.py').write_text('print("runtime")\n' * 30)
        (self.root / 'README.md').write_text('documentation\n')
        self.commit()
        self.base = self.git('rev-parse', 'HEAD').strip()

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root, text=True)

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'fixture')

    def detect(self, base=None):
        return subprocess.run(['python3', str(SCRIPT)], cwd=self.root,
            env={**os.environ, 'BASE_SHA': self.base if base is None else base, 'HEAD_SHA': 'HEAD'},
            capture_output=True, text=True)

    def test_docs_only(self):
        (self.root / 'README.md').write_text('updated documentation\n')
        self.commit()
        result = self.detect()
        self.assertEqual((result.returncode, result.stdout.strip()), (0, 'false'))

    def test_rename_runtime_into_docs(self):
        (self.root / 'docs').mkdir()
        (self.root / 'runtime.py').rename(self.root / 'docs/runtime.md')
        self.commit()
        self.git('config', 'diff.renames', 'true')
        result = self.detect()
        self.assertEqual((result.returncode, result.stdout.strip()), (0, 'true'))

    def test_missing_base_fails(self):
        self.assertNotEqual(self.detect('a' * 40).returncode, 0)

    def test_initial_base_builds(self):
        for base in ['', '0' * 40]:
            with self.subTest(base=base):
                result = self.detect(base)
                self.assertEqual((result.returncode, result.stdout.strip()), (0, 'true'))

    def test_actual_workflow_guard_propagates_failure(self):
        text = WORKFLOW.read_text()
        block = text.split('        run: |\n', 1)[1].split('  backend:', 1)[0]
        block = '\n'.join(line[10:] if line.startswith(' ' * 10) else line for line in block.splitlines())
        scripts = self.root / '.github/scripts'
        scripts.mkdir(parents=True)
        (scripts / SCRIPT.name).write_bytes(SCRIPT.read_bytes())
        output = self.root / 'output'
        result = subprocess.run(['bash', '-euo', 'pipefail', '-c', block], cwd=self.root,
            env={**os.environ, 'BASE_SHA': 'a' * 40, 'HEAD_SHA': 'HEAD', 'GITHUB_OUTPUT': str(output)},
            capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(output.exists())


if __name__ == '__main__':
    unittest.main()
