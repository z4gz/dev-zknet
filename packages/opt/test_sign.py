"""sign.sh: signs a WalletShield export with the clients' key, and only that key."""

import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
WORKSPACE = HERE.parents[1]
SIGN_SH = HERE / 'sign.sh'
ASSET_KEY = WORKSPACE / 'libs' / 'client-core' / 'asset-key.pub'
BINARIES = ['walletshield-linux-x64', 'walletshield-macos-arm64']
PASSWORD = 's3cret-PASS'


def keypair(where: Path, name: str, password: str | None) -> tuple[str, str]:
    """A throwaway minisign key pair: (public key line, secret key file contents)."""
    pub, sec = where / f'{name}.pub', where / f'{name}.key'
    args = ['minisign', '-G', '-f', '-p', pub, '-s', sec] + ([] if password else ['-W'])
    stdin = f'{password}\n{password}\n' if password else ''
    subprocess.run(args, input=stdin, capture_output=True, text=True, check=True)
    return pub.read_text().splitlines()[1], sec.read_text()


@unittest.skipUnless(shutil.which('minisign'), 'minisign is not installed')
class SignTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        # a workspace of its own: sign.sh, the clients' key, a git commit
        self.ws = self.tmp / 'ws'
        (self.ws / 'packages' / 'opt').mkdir(parents=True)
        shutil.copy(SIGN_SH, self.ws / 'packages' / 'opt' / 'sign.sh')
        (self.ws / 'libs' / 'client-core').mkdir(parents=True)
        self.public, self.secret = keypair(self.tmp, 'clients', PASSWORD)
        (self.ws / 'libs' / 'client-core' / 'asset-key.pub').write_text(self.public + '\n')
        git = ['git', '-C', self.ws, '-c', 'user.name=t', '-c', 'user.email=t@t']
        subprocess.run([*git, 'init', '-q'], check=True)
        subprocess.run([*git, 'add', '.'], check=True)
        subprocess.run([*git, 'commit', '-qm', 'x'], check=True)
        subprocess.run([*git, 'tag', 'v1.2.3'], check=True)
        # an export at the default place
        self.served = self.ws / 'dist' / 'woodpecker' / 'walletshield' / 'served'
        self.served.mkdir(parents=True)
        for b in BINARIES:
            (self.served / b).write_text(b)
        subprocess.run(f'sha256sum {" ".join(BINARIES)} > checksums.txt', shell=True, check=True, cwd=self.served)

    def sign(self, *args, key=None, password=PASSWORD):
        env = {'PATH': '/usr/bin:/bin:/usr/local/bin', 'HOME': str(self.tmp)}
        if key is not None:
            env['ZKNET_ASSET_SIGNING_KEY'] = key
        if password is not None:
            env['ZKNET_ASSET_SIGNING_PASSWORD'] = password
        return subprocess.run([self.ws / 'packages' / 'opt' / 'sign.sh', *args], capture_output=True,
                              text=True, env=env)

    def signature(self) -> Path:
        return self.served / 'checksums.txt.minisig'

    def assertNoSecret(self, result, secret: str):
        for part in [PASSWORD, *secret.splitlines()[1:]]:
            self.assertNotIn(part, result.stdout + result.stderr)

    def test_signs_the_default_export_with_the_clients_key(self):
        result = self.sign(key=self.secret)
        self.assertEqual(result.returncode, 0, result.stderr)
        lines = result.stdout.splitlines()
        self.assertEqual(lines[0], 'sign: checksums.txt: 2 binaries match their hashes')
        self.assertRegex(lines[1], r'^sign: signed for build v1\.2\.3 \(commit [0-9a-f]+\) with key [0-9A-F]{16}$')
        self.assertEqual(lines[2], 'sign: verified against libs/client-core/asset-key.pub')
        verified = subprocess.run(['minisign', '-V', '-P', self.public, '-m', self.served / 'checksums.txt'],
                                  capture_output=True, text=True)
        self.assertEqual(verified.returncode, 0, verified.stderr)
        self.assertRegex(verified.stdout, r'Trusted comment: zknet-assets v1 build=v1\.2\.3 commit=[0-9a-f]+ timestamp:\d+')
        self.assertNoSecret(result, self.secret)

    def test_prints_the_key_id_minisign_prints(self):
        result = self.sign(key=self.secret)
        key_id = result.stdout.split('with key ')[1].split()[0]
        pub_comment = (self.tmp / 'clients.pub').read_text().splitlines()[0]
        self.assertTrue(pub_comment.endswith(key_id), pub_comment)

    def test_signs_an_export_named_on_the_command_line(self):
        elsewhere = self.tmp / 'served'
        shutil.copytree(self.served, elsewhere)
        result = self.sign(str(elsewhere), key=self.secret)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((elsewhere / 'checksums.txt.minisig').is_file())
        self.assertFalse(self.signature().exists())

    def test_an_unencrypted_key_needs_no_passphrase(self):
        public, secret = keypair(self.tmp, 'plain', None)
        (self.ws / 'libs' / 'client-core' / 'asset-key.pub').write_text(public + '\n')
        result = self.sign(key=secret, password=None)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_refuses_a_key_that_is_not_the_clients(self):
        _, other = keypair(self.tmp, 'other', PASSWORD)
        result = self.sign(key=other)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("this is not the clients' key", result.stderr)
        self.assertFalse(self.signature().exists())
        self.assertNoSecret(result, other)

    def test_a_wrong_passphrase_leaves_no_signature_and_no_secret(self):
        result = self.sign(key=self.secret, password='wrong-passphrase')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sign: minisign could not sign', result.stderr)
        self.assertFalse(self.signature().exists())
        self.assertNoSecret(result, self.secret)

    def test_refuses_binaries_that_do_not_match_the_list(self):
        (self.served / BINARIES[0]).write_text('tampered')
        result = self.sign(key=self.secret)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('do not match checksums.txt', result.stderr)
        self.assertFalse(self.signature().exists())

    def test_refuses_without_a_key_or_an_export(self):
        result = self.sign()
        self.assertIn('ZKNET_ASSET_SIGNING_KEY is not set', result.stderr)
        result = self.sign(str(self.tmp / 'nowhere'), key=self.secret)
        self.assertIn('build the export with `.woodpecker/run dist`', result.stderr)


class AssetKeyTest(unittest.TestCase):
    def test_the_clients_key_is_one_line_read_by_both_sides(self):
        lines = ASSET_KEY.read_text().splitlines()
        self.assertEqual(len(lines), 1)
        self.assertRegex(lines[0], r'^RW[A-Za-z0-9+/]{54}$')
        self.assertIn('include_str!("../asset-key.pub")', (WORKSPACE / 'libs/client-core/src/auth.rs').read_text())
        self.assertIn('libs/client-core/asset-key.pub', SIGN_SH.read_text())


if __name__ == '__main__':
    unittest.main()
