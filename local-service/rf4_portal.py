"""Serve the supplied portal design using the existing local account service."""
from pathlib import Path
from urllib.parse import unquote
import mimetypes

ROOT = Path(__file__).resolve().parent / 'web' / 'portal'

def asset(path):
    if path in ('/', '/portal', '/portal/'):
        target = ROOT / 'index.html'
    elif path.startswith('/portal/') and not path.startswith('/portal/api/'):
        target = ROOT / unquote(path[len('/portal/'):])
    else:
        return None
    target = target.resolve()
    if not target.is_relative_to(ROOT.resolve()) or not target.is_file():
        return (404, b'Not found', 'text/plain; charset=utf-8')
    kind = mimetypes.guess_type(target.name)[0] or 'application/octet-stream'
    if kind.startswith('text/') or kind in ('application/javascript',):
        kind += '; charset=utf-8'
    return (200, target.read_bytes(), kind)
