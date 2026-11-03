"""Local preset settings and a thin wrapper around the Go membership service."""
from contextlib import contextmanager
import json
import math
from pathlib import Path
import sqlite3
import subprocess
import time
from uuid import UUID


DEBUG_VALUES = {'bite_delay_fixed': '0', 'bite_delay_scale': '1',
                'bite_delay_max': '5', 'fish_weight_scale': '4'}
DEBUG_STATE_KEY = 'local_debug_state_v1'
PLAN_IDS = {'premium_1d', 'premium_7d', 'premium_30d'}


class LocalOptionsError(Exception):
    def __init__(self, status, message):
        self.status, self.message = status, message


@contextmanager
def game_database(path):
    db = None
    try:
        db = sqlite3.connect(Path(path).resolve().as_uri() + '?mode=rw', uri=True, timeout=5)
        db.execute('PRAGMA foreign_keys=ON')
        with db:
            yield db
    except sqlite3.Error:
        raise LocalOptionsError(503, '本地游戏数据库暂不可用') from None
    finally:
        if db is not None:
            db.close()


def _state(raw):
    if raw is None:
        return None
    try:
        value = json.loads(raw)
        if (value.get('schema') != 1 or type(value.get('enabled')) is not bool
                or set(value.get('originals', {})) != set(DEBUG_VALUES)
                or any(v is not None and not isinstance(v, str) for v in value['originals'].values())):
            raise ValueError()
        return value
    except (ValueError, AttributeError, TypeError):
        raise LocalOptionsError(503, '调试设置记录无效，未覆盖原参数') from None


def _put(db, key, value):
    db.execute('INSERT INTO tunables(key,value,updated_by,updated_ts) VALUES(?,?,?,?) '
               'ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_by=excluded.updated_by,'
               'updated_ts=excluded.updated_ts', (key, value, 'local-settings', time.time()))


def _debug_status(values):
    state = _state(values.get(DEBUG_STATE_KEY))
    enabled = values.get('debug_mode', '0') == '1'
    if state is not None and state['enabled'] != enabled:
        raise LocalOptionsError(503, '调试设置不一致，请检查本地配置')
    raw_weight = values.get('fish_weight_scale', '1')
    try:
        weight = float(raw_weight)
        if not math.isfinite(weight) or weight <= 0:
            raise ValueError()
    except ValueError:
        raise LocalOptionsError(503, '鱼体重参数无效') from None
    return {'enabled': enabled, 'configured': state is not None, 'name': '调试模式',
            'biteMinSeconds': 3 if enabled else None, 'biteMaxSeconds': 5 if enabled else None,
            'weightMultiplier': weight, 'equipmentLimits': True, 'refreshSeconds': 30}


def debug_status(path):
    with game_database(path) as db:
        return _debug_status(dict(db.execute('SELECT key,value FROM tunables')))


def set_debug(path, enabled, *, only_if_missing=False):
    if type(enabled) is not bool:
        raise LocalOptionsError(400, '启用状态须为布尔值')
    with game_database(path) as db:
        db.execute('BEGIN IMMEDIATE')
        values = dict(db.execute('SELECT key,value FROM tunables'))
        state = _state(values.get(DEBUG_STATE_KEY))
        if only_if_missing and state is not None:
            if not state['enabled'] or all(values.get(key) == value for key, value in DEBUG_VALUES.items()):
                return _debug_status(values)
        if enabled:
            if state is None or not state['enabled']:
                state = {'schema': 1, 'enabled': True,
                         'originals': {key: values.get(key) for key in DEBUG_VALUES}}
            for key, value in DEBUG_VALUES.items():
                _put(db, key, value)
        elif state is not None and state['enabled']:
            for key, value in state['originals'].items():
                if value is None:
                    db.execute('DELETE FROM tunables WHERE key=?', (key,))
                else:
                    _put(db, key, value)
        if state is None:
            state = {'schema': 1, 'enabled': False,
                     'originals': {key: values.get(key) for key in DEBUG_VALUES}}
        state['enabled'] = enabled
        _put(db, 'debug_mode', '1' if enabled else '0')
        _put(db, DEBUG_STATE_KEY, json.dumps(state, separators=(',', ':')))
        return _debug_status(dict(db.execute('SELECT key,value FROM tunables')))


def ensure_debug_defaults(path):
    return set_debug(path, True, only_if_missing=True)


def membership(database, reference_dir, uid, *, plan_id=None, request_id=None):
    if type(uid) is not int or not 0 < uid <= 2**63 - 1:
        raise LocalOptionsError(403, '本地账户未绑定游戏角色')
    if not Path(database).is_file():
        raise LocalOptionsError(503, '本地游戏角色尚未就绪，请先启动本地游戏后台')
    command = [str(Path(reference_dir) / 'bin' / 'local-membership.exe'),
               '--database', str(Path(database).resolve()), '--uid', str(uid)]
    if plan_id is None:
        command.append('status')
    else:
        if not isinstance(plan_id, str) or plan_id not in PLAN_IDS:
            raise LocalOptionsError(400, '请选择有效的会员套餐')
        try:
            if not isinstance(request_id, str) or str(UUID(request_id)) != request_id.lower():
                raise ValueError()
        except (ValueError, AttributeError):
            raise LocalOptionsError(400, '购买请求编号无效') from None
        command.extend(['purchase', '--plan', plan_id, '--request-id', str(UUID(request_id))])
    try:
        result = subprocess.run(command, cwd=reference_dir, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                encoding='utf-8', errors='replace', timeout=10,
                                creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        value = json.loads(result.stdout)
    except (OSError, subprocess.TimeoutExpired, ValueError):
        raise LocalOptionsError(503, '本地会员服务暂不可用，请稍后重试同一次请求') from None
    if not isinstance(value, dict):
        raise LocalOptionsError(503, '本地会员服务返回无效')
    if not value.get('ok'):
        error = value.get('error')
        code = error.get('code') if isinstance(error, dict) else None
        errors = {'invalid_plan': (400, '请选择有效的会员套餐'),
                  'invalid_request_id': (400, '购买请求编号无效'),
                  'insufficient_gold': (409, '游戏金币不足，本次未扣款'),
                  'user_not_found': (404, '未找到该本地游戏角色'),
                  'request_conflict': (409, '请求编号已用于另一笔购买，请先核对会员状态'),
                  'already_permanent': (409, '当前角色已拥有永久会员，无需购买')}
        status, message = errors.get(code, (503, '本地会员服务暂不可用，请稍后重试同一次请求'))
        raise LocalOptionsError(status, message)
    if result.returncode != 0:
        raise LocalOptionsError(503, '本地会员服务返回无效')
    try:
        state, balance, plans = value['membership'], value['balance'], value['plans']
        if (type(state['active']) is not bool or type(state['permanent']) is not bool
                or (state['expiresAt'] is not None and type(state['expiresAt']) is not int)
                or not isinstance(state['xpBonusRate'], (int, float))
                or isinstance(state['xpBonusRate'], bool) or not math.isfinite(state['xpBonusRate'])
                or not 0 <= state['xpBonusRate'] <= 10
                or any(type(balance[key]) is not int for key in ('gold', 'silver'))
                or not isinstance(plans, list)):
            raise ValueError()
        for plan in plans:
            if (plan['id'] not in PLAN_IDS or not isinstance(plan['name'], str)
                    or type(plan['days']) is not int or plan['days'] <= 0
                    or type(plan['priceGold']) is not int or plan['priceGold'] < 0):
                raise ValueError()
    except (KeyError, TypeError, ValueError):
        raise LocalOptionsError(503, '本地会员状态格式无效') from None
    return value
