"""Serve the existing portal design with the local account identity."""
import ipaddress
import mimetypes
from pathlib import Path
import time
from urllib.parse import unquote
from rf4_local_options import LocalOptionsError, game_database
from rf4_local_membership import membership

ROOT = Path(__file__).resolve().parent / 'web' / 'portal'
SHARED = Path(__file__).resolve().parents[1] / 'internal' / 'portal' / 'web'
SHARED_CSS = {'framework.css', 'portal.css', 'reference-ui.css', 'local.css'}


def asset(path):
    if path in ('/', '/portal', '/portal/'):
        target = ROOT / 'index.html'
    elif path.startswith('/portal/') and not path.startswith('/portal/api/'):
        name = unquote(path[len('/portal/'):])
        if name in SHARED_CSS:
            target = SHARED / name
        elif name in {'index.html', 'local.js'}:
            target = ROOT / name
        else:
            return 404, b'Not found', 'text/plain; charset=utf-8'
    else:
        return None
    if not target.is_file():
        return 404, b'Not found', 'text/plain; charset=utf-8'
    kind = mimetypes.guess_type(target.name)[0] or 'application/octet-stream'
    if kind.startswith('text/') or kind in ('application/javascript',):
        kind += '; charset=utf-8'
    return 200, target.read_bytes(), kind


class PortalMixin:
    def portal_local(self):
        try:
            local = ipaddress.ip_address(self.client_address[0]).is_loopback
        except ValueError:
            local = False
        if not local:
            raise LocalOptionsError(403, '请在本机打开个人中心')

    def portal_user(self, resolve_identity):
        self.portal_local()
        with game_database(self.server.db_path) as db:
            name, _ = self.current_user(db)
            rowid = db.execute('SELECT rowid FROM accounts WHERE username=?', (name,)).fetchone()[0]
        uid = resolve_identity(self.server, name, rowid)
        if uid is None:
            raise LocalOptionsError(403, '本地账户尚未绑定游戏角色')
        return name, uid

    def portal_request(self, path, method, query, resolve_identity):
        if method == 'GET':
            static = asset(path)
            if static is not None:
                self.portal_local()
                self.reply(*static)
                return True
        if path in ('/api/membership', '/api/membership/purchase'):
            _, uid = self.portal_user(resolve_identity)
            if query:
                raise LocalOptionsError(400, '角色由登录账户确定，不接受查询参数')
            database = self.server.db_path.parent / 'gameplay.sqlite3'
            manager = getattr(self.server, 'backend_process', None)
            reference = manager.reference_dir if manager else Path(__file__).resolve().parents[1]
            if path == '/api/membership' and method == 'GET':
                value = membership(database, reference, uid)
            elif path == '/api/membership/purchase' and method == 'POST':
                data = self.body()
                if set(data) != {'planID', 'requestID'}:
                    raise LocalOptionsError(400, '购买只接受套餐和请求编号')
                value = membership(database, reference, uid, plan_id=data['planID'], request_id=data['requestID'])
            else:
                raise LocalOptionsError(405, '会员接口不支持此操作')
            self.reply(200, value)
            return True
        if path not in ('/api/profile', '/portal/api/profile'):
            return False
        name, uid = self.portal_user(resolve_identity)
        if query:
            raise LocalOptionsError(400, '角色由登录账户确定，不接受查询参数')
        if method != 'GET':
            raise LocalOptionsError(405, '资料接口只支持读取')
        with game_database(self.server.db_path.parent / 'gameplay.sqlite3') as db:
            row = db.execute('SELECT name,level,stars,region,gold,silver,membership,membership_exp '
                             'FROM profile WHERE uid=?', (uid,)).fetchone()
        if row is None:
            raise LocalOptionsError(404, '请先进入游戏创建本地角色')
        permanent = row[6] == 2
        active = permanent or row[6] == 1 and row[7] > time.time()
        self.reply(200, {'username': name, 'userId': uid, 'nickname': row[0], 'level': row[1],
            'stars': row[2], 'region': row[3], 'goldMinor': row[4], 'silverMinor': row[5],
            'premium': active, 'premiumUntil': row[7]*1000 if row[7] and not permanent else None})
        return True
