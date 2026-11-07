"""Call the existing local-currency membership CLI for the session's game identity."""
import json
import math
from pathlib import Path
import subprocess
from uuid import UUID
from rf4_local_options import LocalOptionsError

PLAN_IDS = {'premium_1d', 'premium_7d', 'premium_30d'}


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
                or type(state['expiresAt']) is not int or state['expiresAt'] < 0
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
    if plan_id is not None:
        try:
            receipt = value['purchase']
            if (receipt['requestID'].lower() != request_id.lower() or receipt['planID'] != plan_id
                    or type(receipt['chargedGold']) is not int or receipt['chargedGold'] < 0
                    or type(receipt['expiresAt']) is not int or receipt['expiresAt'] <= 0
                    or type(receipt['replayed']) is not bool):
                raise ValueError()
        except (KeyError, AttributeError, TypeError, ValueError):
            raise LocalOptionsError(503, '购买结果未能确认，请重试同一次请求') from None
    return value
