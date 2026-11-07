"""Access existing local game data without creating a new database."""
from contextlib import contextmanager
from pathlib import Path
import sqlite3
import json
import math
import time


class LocalOptionsError(Exception):
    def __init__(self, status, message):
        self.status, self.message = status, message


@contextmanager
def game_database(path, *, writable=False):
    db = None
    try:
        mode = 'rw' if writable else 'ro'
        db = sqlite3.connect(Path(path).resolve().as_uri() + '?mode=' + mode, uri=True, timeout=5)
        db.execute('PRAGMA foreign_keys=ON')
        with db:
            yield db
    except sqlite3.Error:
        raise LocalOptionsError(503, '本地游戏数据库暂不可用') from None
    finally:
        if db is not None:
            db.close()


DEBUG_VALUES = {'bite_delay_fixed': '0', 'bite_delay_scale': '1',
                'bite_delay_max': '5', 'fish_weight_scale': '4'}
DEBUG_STATE_KEY = 'local_debug_state_v1'


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
    try:
        matches = all(float(values.get(key, 'nan')) == float(value) for key, value in DEBUG_VALUES.items())
    except (ValueError, TypeError):
        matches = False
    return {'enabled': enabled, 'configured': state is not None, 'name': '调试模式',
            'presetActive': enabled and matches,
            'weightMultiplier': weight, 'equipmentLimits': True, 'refreshSeconds': 30}


def debug_status(path):
    with game_database(path) as db:
        return _debug_status(dict(db.execute('SELECT key,value FROM tunables')))


def set_debug(path, enabled):
    if type(enabled) is not bool:
        raise LocalOptionsError(400, '启用状态须为布尔值')
    with game_database(path, writable=True) as db:
        db.execute('BEGIN IMMEDIATE')
        values = dict(db.execute('SELECT key,value FROM tunables'))
        state = _state(values.get(DEBUG_STATE_KEY))
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
