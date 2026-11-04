"""Stage tracked release resources using an explicit file allowlist."""
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys

LOCAL_FILES = {
    'local_server.py', 'rf4_admin.py', 'rf4_backend.py', 'rf4_local_options.py', 'rf4_local_membership.py',
    'rf4_phoenix.py', 'rf4_portal.py', 'rf4_service_process.py', 'rf4_wire.py',
    '使用说明.txt', '启动本地服务.cmd',
}
PORTAL_FILES = {'index.html', 'local.js'}
PORTAL_CSS = {'framework.css', 'local.css', 'portal.css', 'reference-ui.css'}
STATIC_FILES = {'server_data/static.sqlite3', 'server_data/compat_parts.json'}

def allowed(name):
    p = PurePosixPath(name)
    if p.is_absolute() or '..' in p.parts:
        return False
    if name in {'README.md', 'LICENSE', 'config.example.yaml'} | STATIC_FILES:
        return True
    if p.parts[0:1] == ('docs',) and p.suffix == '.md':
        return True
    if p.parent == PurePosixPath('internal/portal/web'):
        return p.name in PORTAL_CSS
    if p.parent == PurePosixPath('local-service'):
        return p.name in LOCAL_FILES
    return p.parent == PurePosixPath('local-service/web/portal') and p.name in PORTAL_FILES

def stage(root, target, tracked):
    root, target = Path(root).resolve(), Path(target).resolve()
    copied = []
    for name in tracked:
        if not allowed(name):
            continue
        source = root / name
        if source.is_symlink() or not source.is_file() or not source.resolve().is_relative_to(root):
            raise ValueError('Invalid release source: ' + name)
        dest = target / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, dest)
        copied.append(name)
    return copied

if __name__ == '__main__':
    root = Path(__file__).resolve().parents[1]
    tracked = subprocess.check_output(['git', 'ls-files', '-z'], cwd=root).decode().split('\0')
    copied = stage(root, sys.argv[1], filter(None, tracked))
    print('Staged', len(copied), 'tracked resource files')
