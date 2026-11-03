"""Same-origin adapter for the existing Go administration service."""
from contextlib import closing
import http.client
import ipaddress
import json
import sqlite3
from pathlib import Path
from urllib.parse import urlsplit

from rf4_local_options import debug_status, set_debug


def proxy(handler, path, resolve_uid):
    process = getattr(handler.server, 'backend_process', None)
    address = getattr(process, 'admin_address', None)
    if not address:
        return 503, {'error': '管理后台尚未就绪，请重新启动本地服务'}, 'application/json; charset=utf-8'
    if hasattr(process, 'check_alive'):
        try:
            process.check_alive()
        except RuntimeError:
            return 503, {'error': '本地后台已停止，请重新启动'}, 'application/json; charset=utf-8'
    host, port = address
    if not ipaddress.ip_address(host).is_loopback:
        raise ValueError('Admin destination must be loopback')
    target = path[len('/admin'):] or '/'
    query = urlsplit(handler.path).query
    if query:
        target += '?' + query
    method = handler.command
    if method not in ('GET', 'POST', 'PUT'):
        return 405, {'error': '不支持此操作'}, 'application/json; charset=utf-8'
    headers = {'Authorization': handler.headers.get('Authorization', '')}
    special = path in ('/admin/api/debug', '/admin/api/accounts')
    connection = http.client.HTTPConnection(host, port, timeout=12)
    try:
        if special:
            connection.request('GET', '/api/me', headers=headers)
            response = connection.getresponse()
            body = response.read()
            if response.status != 200:
                return response.status, body, 'application/json; charset=utf-8'
            if path.endswith('/debug'):
                if method == 'GET':
                    result = debug_status(process.database)
                elif method == 'PUT':
                    data = handler.body()
                    if set(data) != {'enabled'}:
                        return 400, {'error': '请选择启用或关闭调试模式'}, 'application/json; charset=utf-8'
                    result = set_debug(process.database, data['enabled'])
                else:
                    return 405, {'error': '不支持此操作'}, 'application/json; charset=utf-8'
                return 200, result, 'application/json; charset=utf-8'
            if method != 'GET':
                return 405, {'error': '本地账号列表只读'}, 'application/json; charset=utf-8'
            with closing(sqlite3.connect(Path(handler.server.db_path).resolve().as_uri()+'?mode=ro', uri=True)) as db:
                accounts = [{'username': name, 'uid': resolve_uid(handler.server, name, rowid),
                             'passwordState': '已设置'}
                            for rowid, name in db.execute('SELECT rowid,username FROM accounts ORDER BY username')]
            return 200, accounts, 'application/json; charset=utf-8'
        body = None
        if method in ('POST', 'PUT'):
            body = json.dumps(handler.body(), ensure_ascii=False).encode('utf-8')
            headers['Content-Type'] = 'application/json'
        connection.request(method, target, body=body, headers=headers)
        response = connection.getresponse()
        body = response.read(8*1024*1024+1)
        if len(body) > 8*1024*1024:
            raise OSError('Admin response exceeds limit')
        return response.status, body, response.getheader('Content-Type', 'application/octet-stream')
    except (OSError, http.client.HTTPException):
        return 503, {'error': '管理后台暂不可用，请稍后刷新'}, 'application/json; charset=utf-8'
    finally:
        connection.close()
