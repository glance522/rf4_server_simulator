"""Local RF4 account service and authenticated game protocol bridge."""
import argparse
from collections import deque
from contextlib import contextmanager
import hashlib
import hmac
import json
import math
import os
import re
import select
from pathlib import Path
import secrets
import sqlite3
import socketserver
import struct
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit
import webbrowser
import xml.etree.ElementTree as ET
from uuid import UUID
from rf4_wire import Framer, acknowledgement, data_frame, SessionCipher
from rf4_phoenix import Message, Result, Failure, StringArray, GuidArray, NativeValue, IntTriple133, Record309, TypedArray, Reader, UnsupportedValue, encode, decode
from rf4_backend import BackendSession, DATA, CLOSE
from rf4_portal import asset as portal_asset
from rf4_admin import proxy as admin_proxy
from rf4_local_options import LocalOptionsError, debug_status, set_debug, membership

APP_ID = "rf4-local-prototype-v1"
BOOTSTRAP_TYPES = {(1, 13): 206, (5, 1): 311, (11, 1): 136, (6, 2): None,
                   (9, 5): 171, (24, 3): 402, (24, 12): None,
                   (28, 1): 5011, (23, 1): 550, (28, 2): 5003}
BOOTSTRAP_ARRAYS = {(11, 1), (9, 5), (23, 1)}
MAX_BODY = 1024 * 1024
HERMES_ACCEPT = b"DAB12FBC-828D-4E00-B222-FB0D6EE7BF23"
HERMES_REJECT = b"0A095A11-0F24-4EE9-A8D5-197D02754614"
TRANSPORT_KEEPALIVE_SECONDS = 5
TRANSPORT_READ_TIMEOUT_SECONDS = 60
PAGE = r'''<!doctype html><html lang="zh-CN"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>RF4 钓鱼本地版</title><style>
*{box-sizing:border-box}body{margin:0;background:#f3f5f6;color:#202c33;font:14px/1.6 "Microsoft YaHei",sans-serif}
main{max-width:860px;margin:24px auto;padding:0 16px}header{display:flex;gap:10px;align-items:center;justify-content:space-between;flex-wrap:wrap}h1{font-size:22px;margin:0}h2{font-size:16px;margin:0 0 10px}h3{font-size:14px;margin:0 0 4px}
.card{background:white;border:1px solid #d8dfe2;border-radius:8px;padding:16px;margin:12px 0}.notice{border-left:4px solid #397b84;background:#f1f8f8}.badge{font-size:12px;border:1px solid #b7c5ca;border-radius:4px;padding:3px 8px;background:#edf1f3;overflow-wrap:anywhere}.badge.active{color:#795619;background:#fff5dc;border-color:#d6b96f}
p{margin:5px 0}.muted{color:#576b76}.small{font-size:12px}.row{display:flex;gap:12px;flex-wrap:wrap}.field{flex:1;min-width:0}label{display:block;margin-bottom:5px}
input,textarea{width:100%;font:inherit;border:1px solid #aab8c1;border-radius:4px;padding:7px 8px;min-width:0}textarea{min-height:100px;resize:vertical;overflow:auto;font-family:Consolas,"Microsoft YaHei",monospace}
button{font:inherit;border:1px solid #3b606b;border-radius:4px;padding:6px 12px;min-height:36px;background:#236574;color:white;cursor:pointer}button.secondary{background:white;color:#234d58}.actions{display:flex;gap:8px;flex-wrap:wrap;margin-top:10px}
.plans{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;margin-top:12px}.plan{border:1px solid #d8dfe2;border-radius:6px;padding:12px;min-width:0;overflow-wrap:anywhere}.plan button{margin-top:8px;width:100%}.price{font-size:17px;font-weight:600}.metrics{display:flex;gap:6px 22px;flex-wrap:wrap}.metrics p{min-width:0;overflow-wrap:anywhere}.toggle{display:flex;gap:8px;align-items:flex-start;width:fit-content}.toggle input{width:16px;height:16px;flex:0 0 16px;margin:4px 0 0}
#message,#member-message,#debug-message{white-space:pre-wrap;overflow-wrap:anywhere;min-height:0}#message:not(:empty),#member-message:not(:empty),#debug-message:not(:empty){margin-top:8px}#status,#account,#membership-status,#debug-summary{overflow-wrap:anywhere}
button:disabled{opacity:.5;cursor:default}button:focus-visible,input:focus,textarea:focus,summary:focus-visible{outline:2px solid #11849e;outline-offset:2px}summary{cursor:pointer;font-weight:600}details .actions{margin-bottom:0}details[open] summary{margin-bottom:8px}
@media(max-width:600px){.plans{grid-template-columns:1fr}.plan{display:grid;grid-template-columns:minmax(0,1fr) auto;column-gap:10px}.plan h3,.plan p{grid-column:1}.plan button{grid-column:2;grid-row:1/4;align-self:center;width:auto;margin:0;max-width:120px}.row{display:block}.field+.field{margin-top:10px}}
@media(max-width:380px){main{padding:0 10px;margin-top:14px}.card{padding:12px}.plan{display:block}.plan button{width:100%;max-width:none;margin-top:8px}.metrics{display:block}}
</style><main><header><h1>RF4 钓鱼本地版</h1><span class="badge" id="debug-badge">调试模式 · 待读取</span></header><nav class="actions" aria-label="页面入口"><a href="/portal/">个人中心</a><a href="/admin/">打开调试后台</a></nav><p class="muted" id="status">正在检查本地后台……</p>
<section class="card notice"><strong>本地离线游戏 · 测试中</strong><p>已接入地图、背包和钓鱼流程，互动效果正在逐项验证。账号、角色进度和设置保存在本机。</p></section>
<section class="card"><h2>本地账户</h2><p id="account">尚未登录</p><form id="login-form"><div class="row"><div class="field"><label for="username">用户名</label><input id="username" value="123123" autocomplete="username" maxlength="32" required></div><div class="field"><label for="password">本地密码（至少 6 位）</label><input id="password" type="password" autocomplete="current-password" minlength="6" maxlength="128" required></div></div>
<div class="actions"><button type="submit">登录</button><button type="button" class="secondary" id="register">创建账户</button><button type="button" class="secondary" id="logout" disabled>退出账户</button></div></form></section>
<section class="card" id="membership-card"><h2>本地会员</h2><div class="metrics"><p id="membership-status">登录后读取当前角色的会员状态。</p><p id="balance" class="muted">游戏余额：待读取</p></div><p class="muted">会员有效时，鱼获经验额外增加 100%。使用当前角色的游戏金币购买，到期时间与金币扣款保存到本地角色。</p><div class="plans" id="plans"></div><div class="actions"><button class="secondary" id="refresh-member" disabled>刷新会员状态</button></div><p id="member-message" role="status" aria-live="polite"></p></section>
<section class="card"><h2>调试模式</h2><p id="debug-summary">正在读取本地调试设置……</p><label class="toggle" for="debug-enabled"><input id="debug-enabled" type="checkbox" disabled><span>启用调试预设：上鱼间隔约 3–5 秒，鱼体重 4 倍</span></label><p class="muted small">保留落水、钓组就位保护和装备重量上限，实际等待可能延长。仅影响新判定的鱼，不改已有鱼获；搏鱼系数保持原值，大鱼拉力仍会增加。关闭后恢复启用前参数。</p><div class="actions"><button id="debug-save" disabled>保存调试设置</button></div><p id="debug-message" role="status" aria-live="polite"></p></section>
<section class="card"><details><summary>账户测试数据</summary><p class="muted">这里是账户的独立测试数据，游戏角色与鱼获由游戏自动保存。</p><label for="save">测试内容（JSON 对象）</label><textarea id="save" spellcheck="false">{"测试备注":"我的本地存档"}</textarea><div class="actions"><button id="save-button" disabled>保存测试数据</button><button id="load-button" class="secondary" disabled>读取测试数据</button></div></details></section>
<section class="card"><h2>后台控制</h2><p class="muted">关闭网页后后台仍会运行。停止后台不会删除账户或存档。</p><div class="actions"><button id="stop" class="secondary">停止本地后台</button></div><p id="message" role="status" aria-live="polite"></p></section></main>
<script>
let token='',user='',control='',debugLoaded=false,memberState=null,buying=false,pendingPurchase=null;const $=id=>document.getElementById(id);
function message(text){$('message').textContent=text;}
function account(){ $('account').textContent=user?'当前账户：'+user:'尚未登录';['logout','save-button','load-button','refresh-member'].forEach(id=>$(id).disabled=!token);$('debug-enabled').disabled=$('debug-save').disabled=!token||!debugLoaded;if(!token){memberState=null;pendingPurchase=null;$('plans').replaceChildren();$('membership-status').textContent='登录后读取当前角色的会员状态。';$('balance').textContent='游戏余额：待读取';$('member-message').textContent='';} }
async function api(path,method='GET',data){const h={};if(data!==undefined)h['Content-Type']='application/json';if(token)h.Authorization='Bearer '+token;const r=await fetch(path,{method,headers:h,body:data===undefined?undefined:JSON.stringify(data)});const j=await r.json();if(!r.ok){const e=Error(j.error||'请求失败');e.status=r.status;throw e;}return j;}
function money(n){return Number.isSafeInteger(n)?(n/100).toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:2}):'金额超出页面显示范围';}
function showMembership(j){memberState=j;const m=j.membership;$('membership-status').textContent=m.permanent?'永久会员':m.active?'会员有效至 '+new Date(m.expiresAt*1000).toLocaleString('zh-CN',{hour12:false}):'当前未开通会员';$('balance').textContent='金币 '+money(j.balance.gold)+' · 银币 '+money(j.balance.silver);$('plans').replaceChildren();for(const plan of j.plans){const card=document.createElement('div');card.className='plan';const title=document.createElement('h3');title.textContent=plan.name;const price=document.createElement('p');price.className='price';price.textContent=money(plan.priceGold)+' 金币';const detail=document.createElement('p');detail.className='muted small';detail.textContent=plan.days+' 天 · 鱼获经验 +100%';const button=document.createElement('button');button.textContent=m.permanent?'已有永久会员':j.balance.gold<plan.priceGold?'金币不足':pendingPurchase?.planID===plan.id?'重试本次购买':m.active?'购买并续期':'购买会员';button.disabled=buying||m.permanent||j.balance.gold<plan.priceGold||!!pendingPurchase&&pendingPurchase.planID!==plan.id;button.onclick=()=>buy(plan.id);card.append(title,price,detail,button);$('plans').append(card);}}
async function loadMembership(quiet=false){if(!token)return;const boundToken=token;if(!quiet)$('member-message').textContent='正在读取……';try{const j=await api('/api/membership');if(boundToken!==token)return;showMembership(j);if(!quiet)$('member-message').textContent='';}catch(e){if(boundToken!==token)return;memberState=null;$('plans').replaceChildren();$('membership-status').textContent='暂时无法读取会员状态';$('balance').textContent='游戏余额：未知';$('member-message').textContent=e.message;}}
async function buy(planID){if(buying)return;const boundToken=token;if(pendingPurchase&&pendingPurchase.planID!==planID)return;pendingPurchase=pendingPurchase||{planID,requestID:crypto.randomUUID()};buying=true;if(memberState)showMembership(memberState);$('member-message').textContent='正在保存购买……';try{const j=await api('/api/membership/purchase','POST',pendingPurchase);if(boundToken!==token)return;pendingPurchase=null;showMembership(j);$('member-message').textContent=j.purchase?.replayed?'这次购买已处理，未重复扣款。':'购买已保存，游戏内会员状态会自动刷新。';}catch(e){if(boundToken!==token)return;if(e.status&&e.status<500)pendingPurchase=null;$('member-message').textContent=e.message+(pendingPurchase?'；请重试本次购买，勿重复新建订单。':'');}finally{buying=false;if(boundToken===token&&memberState)showMembership(memberState);}}
function showDebug(j){debugLoaded=true;$('debug-enabled').checked=j.enabled;$('debug-badge').textContent=j.enabled?'调试模式 · 已启用':'调试模式 · 已关闭';$('debug-badge').className='badge'+(j.enabled?' active':'');$('debug-summary').textContent=j.enabled?'预设：上鱼间隔约 '+j.biteMinSeconds+'–'+j.biteMaxSeconds+' 秒，鱼体重 '+j.weightMultiplier+' 倍。':'当前使用原参数，鱼体重倍率 '+j.weightMultiplier+'。';account();}
async function loadDebug(){try{showDebug(await api('/api/debug'));}catch(e){debugLoaded=false;$('debug-badge').textContent='调试模式 · 状态未知';$('debug-summary').textContent=e.message;account();}}
async function authenticate(register){try{const j=await api(register?'/api/accounts/register':'/api/session','POST',{username:$('username').value,password:$('password').value});token=j.token;user=j.username;$('password').value='';account();message(register?'本地账户已创建。':'已登录本地账户。');await Promise.all([loadMembership(),loadDebug()]);}catch(e){message(e.message);}}
$('login-form').onsubmit=e=>{e.preventDefault();if(e.target.reportValidity())authenticate(false);};$('register').onclick=()=>{if($('login-form').reportValidity())authenticate(true);};
$('logout').onclick=async()=>{try{await api('/api/session','DELETE');token='';user='';account();message('已退出账户。');}catch(e){message(e.message);}};$('refresh-member').onclick=()=>loadMembership();
$('debug-save').onclick=async()=>{const enabled=$('debug-enabled').checked;$('debug-save').disabled=true;try{showDebug(await api('/api/debug','PUT',{enabled}));$('debug-message').textContent='设置已保存，对后续抛竿生效；后台参数最多 30 秒刷新。';}catch(e){$('debug-message').textContent=e.message;}finally{account();}};
$('save-button').onclick=async()=>{try{const value=JSON.parse($('save').value);await api('/api/save','PUT',{data:value});message('测试数据已保存到本机。');}catch(e){message(e instanceof SyntaxError?'JSON 格式有误，请检查后保存。':e.message);}};$('load-button').onclick=async()=>{try{const j=await api('/api/save');$('save').value=JSON.stringify(j.data,null,2);message('已读取测试数据。');}catch(e){message(e.message);}};
$('stop').onclick=async()=>{try{await api('/api/shutdown','POST',{control});$('status').textContent='本地后台已停止';message('再次双击“启动游戏.exe”可启动。');document.querySelectorAll('button').forEach(b=>b.disabled=true);}catch(e){message(e.message);}};
api('/api/health').then(j=>{control=j.control;$('status').textContent='本地后台运行中 · '+location.host;}).catch(()=>{$('status').textContent='后台无法连接';});loadDebug();account();
</script></html>'''


@contextmanager
def connect(path):
    db = sqlite3.connect(path, timeout=10)
    try:
        db.execute("PRAGMA foreign_keys=ON")
        with db:
            yield db
    finally:
        db.close()


def initialize(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    with connect(path) as db:
        db.execute("BEGIN IMMEDIATE")
        version = db.execute("PRAGMA user_version").fetchone()[0]
        if version not in (0, 1, 2, 3):
            raise RuntimeError("数据库版本不支持，未修改现有数据")
        if version == 0:
            db.execute('''CREATE TABLE accounts(username TEXT PRIMARY KEY COLLATE NOCASE,
                salt BLOB NOT NULL, digest BLOB NOT NULL)''')
            db.execute('''CREATE TABLE sessions(digest TEXT PRIMARY KEY,username TEXT NOT NULL
                REFERENCES accounts(username),expires INTEGER NOT NULL)''')
            db.execute('''CREATE TABLE saves(username TEXT PRIMARY KEY REFERENCES accounts(username),
                data TEXT NOT NULL,updated INTEGER NOT NULL)''')
            db.execute("PRAGMA user_version=1")
            version = 1
        if version == 1:
            db.execute("ALTER TABLE accounts ADD COLUMN native_digest BLOB")
            db.execute("PRAGMA user_version=2")
            version = 2
        if version == 2:
            db.execute('''CREATE TABLE native_states(username TEXT NOT NULL PRIMARY KEY COLLATE NOCASE
                REFERENCES accounts(username),data TEXT NOT NULL,updated INTEGER NOT NULL)''')
            db.execute("PRAGMA user_version=3")
        db.execute("DELETE FROM sessions WHERE expires<=?", (int(time.time()),))


def password_digest(password, salt):
    return hashlib.pbkdf2_hmac("sha256", password.encode(), salt, 600_000)


def native_password_digest(sha1_text, salt):
    return password_digest("rf4-native-sha1:" + sha1_text.lower(), salt)


def native_digest_from_password(password, salt):
    return native_password_digest(hashlib.sha1(password.encode()).hexdigest(), salt)


def load_catalog(directory):
    index_path = directory / 'index.json'
    if not index_path.exists():
        return None
    index = json.loads(index_path.read_text(encoding='utf-8'))
    if index.get('schema') != 1 or index.get('clientVersion') != '4.0.25034':
        raise RuntimeError('基础资料版本不支持')
    entries = index.get('entries', [])
    if len(entries) != 9:
        raise RuntimeError('基础资料数量不完整')
    catalog = {}
    for item in entries:
        resource_id = item.get('id')
        name, version = item.get('file', ''), item.get('version', '')
        if (type(resource_id) is not int or resource_id not in range(1, 10) or resource_id in catalog
                or not re.fullmatch(r'[0-9]{3}-[a-f0-9]{40}\.bin', name)
                or not re.fullmatch(r'[a-fA-F0-9]{40}', version)):
            raise RuntimeError('基础资料索引无效')
        path = (directory / name).resolve()
        if path.parent != directory.resolve() or not 0 < path.stat().st_size <= 16*1024*1024:
            raise RuntimeError('基础资料文件无效')
        blob = path.read_bytes()
        digest = hashlib.sha1(blob).hexdigest()
        if len(blob) != item.get('size') or digest != item.get('sha1') or digest != version.lower():
            raise RuntimeError('基础资料校验失败')
        catalog[resource_id] = (version, blob)
    return catalog


def load_character_inventory(directory, source_ids=None):
    path = directory / '123123' / 'inventory.json'
    if not path.exists():
        return {}
    if path.stat().st_size > 16*1024*1024:
        raise RuntimeError('角色物品索引过大')
    saved = json.loads(path.read_text(encoding='utf-8'))
    if (set(saved) != {'schema', 'clientVersion', 'targetAccount', 'sourceCharacter',
                      'sourceUserID', 'countsByKind', 'idsByKind'}
            or saved['schema'] != 2 or saved['clientVersion'] != '4.0.25034'
            or saved['targetAccount'] != '123123'
            or not isinstance(saved['sourceCharacter'], str)
            or type(saved['sourceUserID']) is not int or saved['sourceUserID'] <= 0):
        raise RuntimeError('角色物品索引版本或账号无效')
    counts, identifiers = saved['countsByKind'], saved['idsByKind']
    kinds = {str(kind) for kind in list(range(12))+[14, 17, 18, 19]}
    if set(counts) != kinds or set(identifiers) != kinds:
        raise RuntimeError('角色物品类别不完整')
    inventory = {}
    for kind in kinds:
        fields, values = counts[kind], identifiers[kind]
        if (set(fields) != {'a', 'b', 'c'}
                or any(type(value) is not int or not 0 <= value <= 2147483647 for value in fields.values())
                or not isinstance(values, list) or len(values) > 100000):
            raise RuntimeError('角色物品数量无效')
        ids = [UUID(value) for value in values]
        if any(value.int == 0 for value in ids) or len(set(ids)) != len(ids):
            raise RuntimeError('角色物品标识无效')
        inventory[int(kind)] = (IntTriple133(**fields), ids)
    if source_ids is not None:
        source_ids['123123'] = saved['sourceUserID']
    return {'123123': inventory}


def load_character_details(directory, inventories):
    directory = directory / '123123'
    path = directory / 'details.json'
    if not path.exists():
        return {}
    if path.stat().st_size > 16*1024*1024:
        raise RuntimeError('角色物品详情索引过大')
    saved = json.loads(path.read_text(encoding='utf-8'))
    if (set(saved) != {'schema', 'clientVersion', 'targetAccount', 'namesRedacted', 'entries'}
            or saved['schema'] != 1 or saved['clientVersion'] != '4.0.25034'
            or saved['targetAccount'] != '123123' or saved['namesRedacted'] is not True
            or '123123' not in inventories):
        raise RuntimeError('角色物品详情版本或账号无效')
    details = {}
    for item in saved['entries']:
        kind = item.get('kind')
        name = item.get('file', '')
        if (type(kind) is not int or kind not in inventories['123123']
                or not re.fullmatch(r'[0-9]{1,2}-[a-f0-9]{64}\.bin', name)):
            raise RuntimeError('角色物品详情索引无效')
        ids = tuple(UUID(value) for value in item.get('ids', []))
        known_ids = inventories['123123'][kind][1]
        if not 0 < len(ids) <= 500 or len(set(ids)) != len(ids) or not set(ids).issubset(known_ids):
            raise RuntimeError('角色物品详情与列表不匹配')
        blob_path = (directory / name).resolve()
        if blob_path.parent != directory.resolve() or not 0 < blob_path.stat().st_size <= 16*1024*1024:
            raise RuntimeError('角色物品详情文件无效')
        blob = blob_path.read_bytes()
        if len(blob) != item.get('size') or hashlib.sha256(blob).hexdigest() != item.get('sha256'):
            raise RuntimeError('角色物品详情校验失败')
        reader = Reader(blob)
        if reader.number('<B') != 3 or reader.short_string() != '61' or reader.number('<H') != len(ids):
            raise RuntimeError('角色物品详情数组无效')
        if (kind, ids) in details:
            raise RuntimeError('角色物品详情重复')
        details[kind, ids] = NativeValue(blob)
    return {'123123': details}


def load_character_profiles(directory, source_ids):
    root = directory.resolve()
    directory = (root / '123123').resolve()
    if directory.parent != root:
        raise RuntimeError('角色资料目录无效')
    path = directory / 'profile.json'
    if not path.exists():
        return {}
    if path.resolve().parent != directory or not 0 < path.stat().st_size <= 65536:
        raise RuntimeError('角色资料索引无效')
    saved = json.loads(path.read_text(encoding='utf-8'))
    fields = {'schema', 'clientVersion', 'targetAccount', 'namesRedacted', 'operation',
              'typeID', 'file', 'sha256', 'size'}
    if (not isinstance(saved, dict) or set(saved) != fields
            or type(saved['schema']) is not int or saved['schema'] != 1
            or saved['clientVersion'] != '4.0.25034' or saved['targetAccount'] != '123123'
            or saved['namesRedacted'] is not True or saved['operation'] != [3, 1]
            or not isinstance(saved['operation'], list)
            or any(type(value) is not int for value in saved['operation'])
            or type(saved['typeID']) is not int or saved['typeID'] != 307):
        raise RuntimeError('角色资料版本或账号无效')
    digest, name, size = saved['sha256'], saved['file'], saved['size']
    if (not isinstance(digest, str) or not re.fullmatch(r'[a-f0-9]{64}', digest)
            or name != f'profile-{digest}.bin'
            or type(size) is not int or not 5 <= size <= 16*1024*1024):
        raise RuntimeError('角色资料文件索引无效')
    source_id = source_ids.get('123123')
    if type(source_id) is not int or not 0 < source_id <= 2**63-1:
        raise RuntimeError('角色来源标识无效')
    blob_path = (directory / name).resolve()
    if blob_path.parent != directory or not blob_path.is_file() or blob_path.stat().st_size != size:
        raise RuntimeError('角色资料文件无效')
    blob = blob_path.read_bytes()
    if (len(blob) != size or hashlib.sha256(blob).hexdigest() != digest
            or blob[:5] != b'\x01'+struct.pack('<i', 307)):
        raise RuntimeError('角色资料校验失败')
    return {'123123': NativeValue(blob)}


def load_character_bootstrap(directory, profiles):
    if '123123' not in profiles:
        return {}
    root = directory.resolve()
    account = (root / '123123').resolve()
    directory = (account / 'bootstrap').resolve()
    if account.parent != root or directory.parent != account:
        raise RuntimeError('角色初始资料目录无效')
    path = directory / 'index.json'
    if not path.exists():
        return {}
    if path.resolve().parent != directory or not 0 < path.stat().st_size <= 65536:
        raise RuntimeError('角色初始资料索引无效')
    saved = json.loads(path.read_text(encoding='utf-8'))
    if (not isinstance(saved, dict)
            or set(saved) != {'schema', 'clientVersion', 'targetAccount', 'namesRedacted', 'entries'}
            or type(saved['schema']) is not int or saved['schema'] != 1
            or saved['clientVersion'] != '4.0.25034' or saved['targetAccount'] != '123123'
            or saved['namesRedacted'] is not True or not isinstance(saved['entries'], list)
            or len(saved['entries']) != len(BOOTSTRAP_TYPES)):
        raise RuntimeError('角色初始资料版本或账号无效')
    bootstrap = {}
    for item in saved['entries']:
        if (not isinstance(item, dict)
                or set(item) != {'operation', 'typeID', 'file', 'sha256', 'size'}
                or not isinstance(item['operation'], list) or len(item['operation']) != 2
                or any(type(value) is not int for value in item['operation'])):
            raise RuntimeError('角色初始资料操作无效')
        operation = tuple(item['operation'])
        if operation not in BOOTSTRAP_TYPES or operation in bootstrap:
            raise RuntimeError('角色初始资料操作重复或不受支持')
        expected_type = BOOTSTRAP_TYPES[operation]
        if (item['typeID'] != expected_type
                or (expected_type is not None and type(item['typeID']) is not int)):
            raise RuntimeError('角色初始资料类型无效')
        digest, name, size = item['sha256'], item['file'], item['size']
        if (not isinstance(digest, str) or not re.fullmatch(r'[a-f0-9]{64}', digest)
                or name != f'{operation[0]}-{operation[1]}-{digest}.bin'
                or type(size) is not int or not 1 <= size <= 16*1024*1024):
            raise RuntimeError('角色初始资料文件索引无效')
        blob_path = (directory / name).resolve()
        if blob_path.parent != directory or not blob_path.is_file() or blob_path.stat().st_size != size:
            raise RuntimeError('角色初始资料文件无效')
        blob = blob_path.read_bytes()
        if len(blob) != size or hashlib.sha256(blob).hexdigest() != digest:
            raise RuntimeError('角色初始资料校验失败')
        try:
            value = decode(blob)
        except UnsupportedValue as error:
            if expected_type is None or error.tag != 1 or error.type_id != expected_type:
                raise RuntimeError('角色初始资料原生类型不匹配') from None
            if operation in BOOTSTRAP_ARRAYS:
                reader = Reader(blob)
                if reader.number('<B') != 3 or not reader.short_string() or reader.number('<H') == 0:
                    raise RuntimeError('角色初始资料数组类型不匹配')
            elif blob[:5] != b'\x01'+struct.pack('<i', expected_type):
                raise RuntimeError('角色初始资料对象类型不匹配')
        except ValueError:
            raise RuntimeError('角色初始资料编码无效') from None
        else:
            if operation == (6, 2):
                valid = value is None
            elif operation == (24, 12):
                valid = isinstance(value, TypedArray)
            else:
                valid = operation in BOOTSTRAP_ARRAYS and isinstance(value, TypedArray) and not value.values
            if not valid:
                raise RuntimeError('角色初始资料结果类型不匹配')
        bootstrap[operation] = NativeValue(blob)
    return {'123123': bootstrap}


def load_character_mailbox(directory, profiles):
    if '123123' not in profiles:
        return {}
    root = directory.resolve()
    directory = (root / '123123').resolve()
    if directory.parent != root:
        raise RuntimeError('角色邮件目录无效')
    path = directory / 'mailbox.json'
    if not path.exists():
        return {}
    if path.resolve().parent != directory or not 0 < path.stat().st_size <= 16*1024*1024:
        raise RuntimeError('角色邮件文件无效')
    saved = json.loads(path.read_text(encoding='utf-8'))
    if (not isinstance(saved, dict)
            or set(saved) != {'schema', 'clientVersion', 'targetAccount', 'operation', 'query', 'ids'}
            or type(saved['schema']) is not int or saved['schema'] != 1
            or saved['clientVersion'] != '4.0.25034' or saved['targetAccount'] != '123123'
            or saved['operation'] != [19, 6] or not isinstance(saved['operation'], list)
            or any(type(value) is not int for value in saved['operation'])
            or saved['query'] != [0, 50] or not isinstance(saved['query'], list)
            or any(type(value) is not int or not -2**31 <= value < 2**31 for value in saved['query'])
            or not isinstance(saved['ids'], list) or len(saved['ids']) > 65535
            or any(not isinstance(value, str) for value in saved['ids'])):
        raise RuntimeError('角色邮件版本或查询无效')
    try:
        identifiers = [UUID(value) for value in saved['ids']]
    except ValueError:
        raise RuntimeError('角色邮件标识无效') from None
    if any(value.int == 0 for value in identifiers) or len(set(identifiers)) != len(identifiers):
        raise RuntimeError('角色邮件标识重复或无效')
    return {'123123': {tuple(saved['query']): GuidArray(identifiers)}}


def save_native_state(path, username, values):
    if len(values) != 2 or not isinstance(values[0], Record309):
        return False
    record, context = values
    fields = {f'f{index}': getattr(record, f'f{index}') for index in range(1, 8)}
    if (any(not isinstance(value, float) or not math.isfinite(value) for value in fields.values())
            or any(type(value) is not int or not -2**31 <= value < 2**31
                   for value in (record.i1, record.i2, context))):
        return False
    state = dict(context=context, **fields, i1=record.i1, i2=record.i2)
    payload = json.dumps(state, allow_nan=False, separators=(',', ':'))
    with connect(path) as db:
        db.execute('''INSERT INTO native_states(username,data,updated) VALUES(?,?,?)
            ON CONFLICT(username) DO UPDATE SET data=excluded.data,updated=excluded.updated''',
                   (username, payload, int(time.time())))
    return True


def inventory_reply(message, inventory, details=None):
    values = message.arguments.values if message.arguments else []
    if message.flag or message.code != 4 or not inventory:
        return None
    if message.subcode == 1 and len(values) == 1 and type(values[0]) is int and values[0] in inventory:
        return Result(message.request_id, inventory[values[0]][0])
    if message.subcode == 4 and len(values) == 3 and all(type(value) is int for value in values):
        kind, start, count = values
        if kind in inventory:
            ids = inventory[kind][1]
            if 0 <= start <= 100000 and 0 <= count <= 500:
                return Result(message.request_id, GuidArray(ids[start:start+count]))
    if (message.subcode == 5 and len(values) == 2 and type(values[0]) is int
            and isinstance(values[1], GuidArray) and details):
        value = details.get((values[0], tuple(values[1].values)))
        if value is not None:
            return Result(message.request_id, value)
    return None


class BadRequest(Exception):
    def __init__(self, status, message):
        self.status, self.message = status, message


class Server(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = False


class GameServer(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = False


class ProtocolDiagnostics:
    """Bounded forwarding metadata and cafe scene IDs; never retain credentials."""
    def __init__(self):
        self.counts = {}
        self.fishing = deque(maxlen=64)
        self.results = deque(maxlen=32)
        self.cafe = deque(maxlen=16)

    def record(self, direction, payload):
        event = {"time": int(time.time()), "direction": direction,
                 "bytes": len(payload), "forwarded": True, "kind": "opaque"}
        if len(payload) >= 9 and payload[0] == 1:
            type_id, request_id = struct.unpack_from('<ii', payload, 1)
            if type_id == -1 and len(payload) >= 12:
                event.update(kind='message', requestID=request_id,
                             code=payload[9], subcode=payload[10])
            elif type_id in (-2, -3):
                event.update(kind='result' if type_id == -2 else 'failure', requestID=request_id)
        key = direction + ':' + event['kind']
        if event['kind'] == 'message':
            key += f":{event['code']}/{event['subcode']}"
        if key not in self.counts and len(self.counts) >= 512:
            key = 'overflow'
        self.counts[key] = self.counts.get(key, 0) + 1
        if event.get('code') == 14:
            self.fishing.append(event)
        elif event['kind'] in ('result', 'failure'):
            self.results.append(event)
        if direction == 'client-to-backend' and (event.get('code'), event.get('subcode')) == (20, 3):
            detail = {name: event[name] for name in ('time', 'requestID')}
            try:
                message = decode(payload)
                values = message.arguments.values if isinstance(message, Message) and message.arguments else []
                detail['argumentTypes'] = [type(value).__name__ for value in values]
                if values and isinstance(values[0], str) and re.fullmatch(r'[A-Za-z0-9_./-]{1,128}', values[0]):
                    detail['scene'] = values[0]
            except (UnsupportedValue, ValueError, IndexError, struct.error):
                detail['decodeFailed'] = True
            self.cafe.append(detail)

    def snapshot(self):
        return {"counts": dict(self.counts), "recentFishing": list(self.fishing),
                "recentResults": list(self.results), "recentCafe": list(self.cafe),
                "valuesRecorded": any('scene' in event for event in self.cafe),
                "recordedValueFields": ['cafe.scene'] if any('scene' in event for event in self.cafe) else []}


def record_forwarded(http, direction, payload):
    with http.events_lock:
        diagnostics = getattr(http, 'protocol_diagnostics', None)
        if diagnostics is None:
            diagnostics = http.protocol_diagnostics = ProtocolDiagnostics()
        diagnostics.record(direction, payload)


def local_game_uid(http, username, rowid):
    identities = getattr(http, 'character_user_ids', {})
    name = username.casefold()
    uid = identities.get(name, rowid)
    if (type(uid) is not int or not 0 < uid <= 2**63 - 1
            or (name not in identities and uid in identities.values())):
        return None
    return uid


class GameHandler(socketserver.BaseRequestHandler):
    def read_exact(self, count):
        result = bytearray()
        while len(result) < count:
            chunk = self.request.recv(count-len(result))
            if not chunk:
                raise EOFError()
            result.extend(chunk)
        return bytes(result)

    def handle(self):
        http = self.server.http
        backend = None
        event = {"time": int(time.time()), "transport": "tcp", "stage": "connected"}
        self.request.settimeout(5)
        try:
            reliable, resume, length = struct.unpack("<BBI", self.read_exact(6))
            if reliable not in (0,1) or resume not in (0,1) or not 1 <= length <= 4096:
                event["stage"] = "invalid-hello"
                return
            key = self.read_exact(length).decode("utf-8")
            digest = hashlib.sha256(key.encode()).hexdigest()
            with connect(http.db_path) as db:
                session = db.execute('''SELECT s.username,a.rowid FROM sessions s JOIN accounts a
                    ON a.username=s.username WHERE s.digest=? AND s.expires>?''',
                    (digest,int(time.time()))).fetchone()
            user_id = local_game_uid(http, session[0], session[1]) if session is not None else None
            valid = session is not None and user_id is not None
            event.update(stage="hello", validLocalSession=session is not None,
                         validLocalIdentity=user_id is not None, reliable=bool(reliable), resume=bool(resume))
            if session is not None and user_id is None:
                event['stage'] = 'local-identity-conflict'
            accepted = valid and not resume
            if valid and resume:
                event['stage'] = 'resume-not-supported'
            self.request.sendall(HERMES_ACCEPT if accepted else HERMES_REJECT)
            if accepted:
                inventory = getattr(http, 'character_inventory', {}).get(session[0].casefold())
                details = getattr(http, 'character_details', {}).get(session[0].casefold())
                profile = getattr(http, 'character_profiles', {}).get(session[0].casefold())
                bootstrap = getattr(http, 'character_bootstrap', {}).get(session[0].casefold(), {}) if profile else {}
                mailbox = getattr(http, 'character_mailbox', {}).get(session[0].casefold(), {}) if profile else {}
                event["stage"] = "handshake-accepted"
                with http.events_lock:
                    http.client_events.append(dict(event))
                framer = Framer()
                negotiated = False
                incoming = outgoing = None
                last_sequence = 0
                server_sequence = 1
                event.update(transportFrames=0, acknowledgedFrames=0, nextPacketBytes=0)
                last_received = last_sent = time.monotonic()

                def send_frame(frame):
                    nonlocal last_sent
                    self.request.settimeout(TRANSPORT_READ_TIMEOUT_SECONDS)
                    self.request.sendall(frame)
                    last_sent = time.monotonic()

                while True:
                    now = time.monotonic()
                    receive_remaining = TRANSPORT_READ_TIMEOUT_SECONDS - (now - last_received)
                    if receive_remaining <= 0:
                        raise TimeoutError()
                    keepalive_remaining = TRANSPORT_KEEPALIVE_SECONDS - (now - last_sent)
                    if keepalive_remaining <= 0:
                        send_frame(b'\0' * 4)
                        keepalive_remaining = TRANSPORT_KEEPALIVE_SECONDS
                    readers = [self.request] + ([backend.socket] if backend else [])
                    readable, _, _ = select.select(readers, [], [], min(receive_remaining, keepalive_remaining))
                    if backend and backend.socket in readable:
                        for kind, _, payload in backend.receive():
                            if kind == CLOSE:
                                event['stage'] = 'backend-session-closed'
                                return
                            if kind == DATA:
                                server_sequence += 1
                                send_frame(data_frame(server_sequence, outgoing.apply(payload)))
                                record_forwarded(http, 'backend-to-client', payload)
                        event.update(time=int(time.time()), stage='backend-data-forwarded')
                    if self.request not in readable:
                        continue
                    following = self.request.recv(65536)
                    last_received = time.monotonic()
                    if not following:
                        event["stage"] = "peer-closed-after-handshake"
                        break
                    event["nextPacketBytes"] += len(following)
                    for frame in framer.feed(following):
                        event["transportFrames"] += 1
                        if frame.kind == 0:
                            if frame.sequence > last_sequence+1:
                                event['stage'] = 'unexpected-data-sequence'
                                return
                            send_frame(acknowledgement(frame.sequence))
                            event["acknowledgedFrames"] += 1
                            event.update(stage="session-protocol-pending", lastSequence=frame.sequence,
                                         lastPayloadBytes=len(frame.payload))
                            if frame.sequence <= last_sequence:
                                continue
                            last_sequence = frame.sequence
                            if not negotiated:
                                if len(frame.payload) > 65536:
                                    event['stage'] = 'negotiation-rejected'
                                    return
                                try:
                                    request = ET.fromstring(frame.payload)
                                except ET.ParseError:
                                    event['stage'] = 'negotiation-rejected'
                                    return
                                if (request.tag != 'hermes' or request.findtext('serializer') != 'phoenix'
                                        or request.findtext('compress', '').lower() != 'false'
                                        or not hmac.compare_digest(request.findtext('token', '').encode(), key.encode())):
                                    event['stage'] = 'negotiation-rejected'
                                    return
                                incoming, outgoing = SessionCipher(key.encode()), SessionCipher(key.encode())
                                backend_config = getattr(http, 'backend_config', None)
                                if backend_config:
                                    backend = BackendSession(*backend_config, user_id, key)
                                    server_sequence = 0
                                else:
                                    ready = Message(1, 0, 1, TypedArray('524', []), True)
                                    send_frame(data_frame(1, outgoing.apply(encode(ready))))
                                negotiated = True
                                event['stage'] = 'hermes-negotiated'
                                with http.events_lock:
                                    http.client_events.append(dict(event))
                                continue
                            plain = incoming.apply(frame.payload)
                            event['time'] = int(time.time())
                            if backend:
                                backend.send_data(plain)
                                record_forwarded(http, 'client-to-backend', plain)
                                event['stage'] = 'rpc-backend-forwarded'
                                for field in ('requestID', 'code', 'subcode'):
                                    event.pop(field, None)
                                if len(plain) >= 12 and plain[:5] == b'\x01\xff\xff\xff\xff':
                                    event['requestID'], event['code'], event['subcode'] = struct.unpack_from('<iBB', plain, 5)
                                with http.events_lock:
                                    http.client_events.append(dict(event))
                                continue
                            for field in ('requestID', 'code', 'subcode', 'argumentTypes', 'messageFlag',
                                          'stringLengths', 'containsClientVersion', 'resourceID', 'missingTag', 'missingTypeID',
                                          'inventoryKind', 'pageStart', 'pageSize', 'itemCount', 'queryNumbers'):
                                event.pop(field, None)
                            try:
                                message = decode(plain)
                            except UnsupportedValue as exc:
                                if (plain[:5] == b'\x01\xff\xff\xff\xff' and len(plain) >= 13
                                        and plain[-1] in (0, 1)):
                                    event['requestID'], event['code'], event['subcode'] = struct.unpack_from('<iBB', plain, 5)
                                    event['messageFlag'] = bool(plain[-1])
                                event.update(stage='rpc-schema-pending', missingTag=exc.tag,
                                             missingTypeID=exc.type_id)
                                if event.get('messageFlag') is False:
                                    result = Failure(event['requestID'], 'System.NotImplementedException',
                                                     '该互动的数据类型尚未接入本地服务')
                                    server_sequence += 1
                                    send_frame(data_frame(server_sequence, outgoing.apply(encode(result))))
                                    event['stage'] = 'rpc-schema-not-implemented-replied'
                                with http.events_lock:
                                    http.client_events.append(dict(event))
                                continue
                            if not isinstance(message, Message):
                                event['stage'] = 'invalid-rpc-message'
                                return
                            values = message.arguments.values if message.arguments else []
                            event.update(stage='rpc-response-pending', requestID=message.request_id,
                                         code=message.code, subcode=message.subcode,
                                         argumentTypes=[type(value).__name__ for value in values],
                                         messageFlag=message.flag,
                                         stringLengths=[len(value) for value in values if isinstance(value, str)],
                                         containsClientVersion='4.0.25034' in values)
                            if message.code == 4 and message.subcode in (1, 4, 5) and values and type(values[0]) is int:
                                event['inventoryKind'] = values[0]
                                if message.subcode == 4 and len(values) == 3 and all(type(value) is int for value in values):
                                    event.update(pageStart=values[1], pageSize=values[2])
                                if message.subcode == 5 and len(values) == 2 and isinstance(values[1], GuidArray):
                                    event['itemCount'] = len(values[1].values)
                            if ((message.code, message.subcode) == (19, 6) and len(values) == 2
                                    and all(type(value) is int for value in values)):
                                event['queryNumbers'] = list(values)
                            result = None
                            catalog = getattr(http, 'catalog', None)
                            if (message.code, message.subcode) == (1, 1) and values == ['4.0.25034'] and not message.flag:
                                result = Result(message.request_id, 'LocalServer/4.0.25034')
                                event['stage'] = 'rpc-version-replied'
                            elif (message.code, message.subcode) == (1, 2) and not values and not message.flag:
                                file_time = 116444736000000000 + time.time_ns() // 100
                                result = Result(message.request_id, file_time)
                                event['stage'] = 'rpc-clock-replied'
                            elif (message.code, message.subcode) == (1, 5) and not values and not message.flag and catalog:
                                result = Result(message.request_id, StringArray([catalog[index][0] for index in range(1, 10)]))
                                event['stage'] = 'rpc-catalog-index-replied'
                            elif ((message.code, message.subcode) == (1, 4) and len(values) == 1
                                  and type(values[0]) is int and not message.flag and catalog and values[0] in catalog):
                                result = Result(message.request_id, catalog[values[0]][1])
                                event.update(stage='rpc-catalog-blob-replied', resourceID=values[0])
                            elif (message.code, message.subcode) == (3, 1) and not values and not message.flag and profile:
                                result = Result(message.request_id, profile)
                                event['stage'] = 'rpc-character-profile-replied'
                            elif not values and not message.flag and (message.code, message.subcode) in bootstrap:
                                result = Result(message.request_id, bootstrap[message.code, message.subcode])
                                event['stage'] = 'rpc-bootstrap-replied'
                            elif ((message.code, message.subcode) == (1, 18) and not message.flag
                                  and profile and inventory and details and len(bootstrap) == len(BOOTSTRAP_TYPES)
                                  and len(values) == 8
                                  and [type(value) for value in values] == [str, bool, bool, int, int, str, str, str]):
                                result = Result(message.request_id, True)
                                event['stage'] = 'rpc-local-initialization-accepted'
                            elif ((message.code, message.subcode) == (19, 6) and not message.flag
                                  and len(values) == 2 and all(type(value) is int for value in values)
                                  and tuple(values) in mailbox):
                                result = Result(message.request_id, mailbox[tuple(values)])
                                event['stage'] = 'rpc-mailbox-replied'
                            elif ((message.code, message.subcode) == (3, 9) and message.flag and profile):
                                try:
                                    saved = save_native_state(http.db_path, session[0], values)
                                except sqlite3.Error:
                                    event['stage'] = 'rpc-native-state-save-failed'
                                else:
                                    event['stage'] = 'rpc-native-state-saved' if saved else 'rpc-native-state-rejected'
                            elif (message.code == 1 and message.subcode in (7, 15) and message.flag
                                  and len(values) == 1 and isinstance(values[0], str)):
                                event['stage'] = 'rpc-oneway-received'
                            elif message.code == 4:
                                result = inventory_reply(message, inventory, details)
                                if result is not None:
                                    event['stage'] = {1: 'rpc-inventory-count-replied', 4: 'rpc-inventory-page-replied',
                                                      5: 'rpc-inventory-details-replied'}[message.subcode]
                            if result is None and not message.flag:
                                result = Failure(message.request_id, 'System.NotImplementedException',
                                                 '该互动尚未接入本地服务')
                                event['stage'] = 'rpc-not-implemented-replied'
                            if result is not None:
                                server_sequence += 1
                                send_frame(data_frame(server_sequence, outgoing.apply(encode(result))))
                            with http.events_lock:
                                http.client_events.append(dict(event))
                        elif frame.kind == 2:
                            event["stage"] = "transport-reset-requested"
                            return
        except ValueError:
            event["stage"] = "invalid-transport-frame"
        except (TimeoutError, EOFError, ConnectionError, UnicodeError, struct.error):
            event["stage"] = "closed-before-session" if event["stage"] == "connected" else event["stage"]
        finally:
            if backend:
                backend.close()
            event['time'] = int(time.time())
            with http.events_lock:
                http.client_events.append(event)


class Handler(BaseHTTPRequestHandler):
    server_version = "RF4LocalPrototype/1"

    def log_message(self, *_):
        pass  # Do not log request URLs, passwords, or tokens.

    def reply(self, status, value, content_type="application/json; charset=utf-8"):
        body = value if isinstance(value, bytes) else value.encode("utf-8") if isinstance(value, str) else json.dumps(value, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.end_headers()
        self.wfile.write(body)

    def body(self):
        if self.headers.get("Content-Type", "").split(";", 1)[0] != "application/json":
            raise BadRequest(415, "请求须使用 JSON 格式")
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            raise BadRequest(400, "请求长度无效")
        if not 0 < length <= MAX_BODY:
            raise BadRequest(413, "请求为空或超过 1 MiB")
        try:
            data = json.loads(self.rfile.read(length))
        except (ValueError, UnicodeError):
            raise BadRequest(400, "JSON 格式有误")
        if not isinstance(data, dict):
            raise BadRequest(400, "请求内容须为 JSON 对象")
        return data

    def current_user(self, db):
        auth = self.headers.get("Authorization", "")
        if not auth.startswith("Bearer "):
            raise BadRequest(401, "请先登录本地账户")
        digest = hashlib.sha256(auth[7:].encode()).hexdigest()
        row = db.execute("SELECT username FROM sessions WHERE digest=? AND expires>?",
                         (digest, int(time.time()))).fetchone()
        if not row:
            raise BadRequest(401, "登录已失效，请重新登录")
        return row[0], digest

    def dispatch(self):
        path = urlsplit(self.path).path
        method = self.command
        query = parse_qs(urlsplit(self.path).query, keep_blank_values=True)
        if not path.startswith("/api/") and path not in ("/", "/portal/", "/portal", "/favicon.ico"):
            with self.server.events_lock:
                self.server.client_events.append({"time": int(time.time()), "method": method,
                    "path": path, "host": self.headers.get("Host", "")[:80],
                    "queryFields": sorted(query),
                    "loginProvided": bool(query.get("login", [""])[0]),
                    "passwordProvided": bool(query.get("password", [""])[0]),
                    "passwordLength": len(query.get("password", [""])[0]),
                    "passwordEncoding": "hex" if re.fullmatch(r"[a-fA-F0-9]+", query.get("password", [""])[0]) else "other"})
                if not hasattr(self.server, 'http_events'):
                    self.server.http_events = deque(maxlen=32)
                self.server.http_events.append(dict(self.server.client_events[-1]))
        host = "127.0.0.1:" + str(self.server.server_port)
        native_target = None
        if path.startswith("/lv58/"):
            native_target = urlsplit(path[len("/lv58/"):])
            native_hosts = {"api.rf4game.ru", "api2.rf4game.ru", "api3.rf4game.ru", "api.rf4game.com", "91.194.110.35", "91.194.110.5"}
            if native_target.scheme not in ("http", "https") or native_target.hostname not in native_hosts:
                raise BadRequest(400, "原生请求目标不受支持")
        accepted_hosts = {host, "127.0.0.1"}
        if path == "/rf4/app/login_notice.json":
            accepted_hosts.update({"91.194.110.35", "cdn7.rf4game.com"})
        if native_target:
            accepted_hosts.update(native_hosts)
        if self.headers.get("Host") not in accepted_hosts:
            raise BadRequest(403, "请通过 127.0.0.1 地址访问")
        origin = self.headers.get("Origin")
        if origin and origin != "http://" + host:
            raise BadRequest(403, "不接受其他网站发来的请求")
        if path == "/admin" or path.startswith("/admin/"):
            return self.reply(*admin_proxy(self, path, local_game_uid))
        if method == "GET":
            static = portal_asset(path)
            if static is not None:
                return self.reply(*static)
            if path == '/local-control/':
                return self.reply(200, PAGE, "text/html; charset=utf-8")
        if method == "GET" and path == "/api/health":
            backend_process = getattr(self.server, 'backend_process', None)
            if backend_process:
                try:
                    backend_process.check_alive()
                except RuntimeError:
                    raise BadRequest(503, '本地游戏业务后台已退出') from None
            protocol = 'reference-experimental' if getattr(self.server, 'backend_config', None) else 'bootstrap-only'
            return self.reply(200, {"app": APP_ID, "status": "ready", "gameProtocol": protocol,
                                    "nativeAuthentication": "experimental", "gamePort": self.server.game_port,
                                    "control": self.server.control})
        if method == "GET" and path == "/rf4/app/login_notice.json":
            return self.reply(200, [])  # The local service publishes no login announcements.
        if method == "GET" and path == "/api/client-events":
            with self.server.events_lock:
                events = list(self.server.client_events)
                diagnostics = getattr(self.server, 'protocol_diagnostics', None)
                protocol = (diagnostics if diagnostics is not None else ProtocolDiagnostics()).snapshot()
                http_events = list(getattr(self.server, 'http_events', []))
            return self.reply(200, {"events": events, "recentHTTP": http_events, "protocol": protocol,
                                    "valuesRecorded": protocol['valuesRecorded']})
        if method == "GET" and native_target and native_target.path == "/login.php":
            return self.native_login(query)
        if method == "GET" and native_target and native_target.path == "/login_token.php":
            return self.native_token_login(query)
        if path in ('/api/debug', '/api/membership', '/api/membership/purchase', '/api/profile'):
            gameplay_path = self.server.db_path.parent / 'gameplay.sqlite3'
            backend_process = getattr(self.server, 'backend_process', None)
            reference = backend_process.reference_dir if backend_process else Path(__file__).resolve().parent.parent
            if method == 'GET' and path == '/api/debug':
                return self.reply(200, debug_status(gameplay_path))
            with connect(self.server.db_path) as db:
                name, _ = self.current_user(db)
                rowid = db.execute('SELECT rowid FROM accounts WHERE username=?', (name,)).fetchone()[0]
            uid = local_game_uid(self.server, name, rowid)
            if uid is None:
                raise BadRequest(403, '本地账户尚未绑定游戏角色')
            if method == 'PUT' and path == '/api/debug':
                data = self.body()
                if set(data) != {'enabled'}:
                    raise BadRequest(400, '只接受调试模式启用状态')
                return self.reply(200, set_debug(gameplay_path, data['enabled']))
            if query:
                raise BadRequest(400, '本地角色由登录账户确定，不接受查询参数')
            if method == 'GET' and path == '/api/profile':
                status = membership(gameplay_path, reference, uid)
                with connect(gameplay_path) as db:
                    row = db.execute('SELECT name,level,stars,region FROM profile WHERE uid=?', (uid,)).fetchone()
                if row is None:
                    raise BadRequest(404, '请先进入游戏创建本地角色')
                state = status['membership']
                return self.reply(200, {'username': name, 'userId': uid, 'nickname': row[0],
                    'level': row[1], 'stars': row[2], 'region': row[3],
                    'goldMinor': status['balance']['gold'], 'silverMinor': status['balance']['silver'],
                    'premium': state['active'], 'premiumUntil': state['expiresAt']*1000 if state['expiresAt'] else None})
            if method == 'GET' and path == '/api/membership':
                return self.reply(200, membership(gameplay_path, reference, uid))
            if method == 'POST' and path == '/api/membership/purchase':
                data = self.body()
                if set(data) != {'planID', 'requestID'}:
                    raise BadRequest(400, '购买只接受套餐和请求编号')
                return self.reply(200, membership(gameplay_path, reference, uid,
                                                 plan_id=data['planID'], request_id=data['requestID']))
            raise BadRequest(405, '本地接口不支持此操作')
        if method == "POST" and path == "/api/shutdown":
            data = self.body()
            if not hmac.compare_digest(str(data.get("control", "")), self.server.control):
                raise BadRequest(403, "后台控制凭据无效")
            self.reply(200, {"status": "stopping"})
            threading.Thread(target=self.server.shutdown, daemon=True).start()
            return
        if path == "/login.php" or path.startswith("/portal/api/"):
            raise BadRequest(501, "游戏协议尚未实现，当前仅支持本地账户与测试存档")
        with connect(self.server.db_path) as db:
            if method == "POST" and path in ("/api/accounts/register", "/api/session"):
                data = self.body()
                name, password = data.get("username", ""), data.get("password", "")
                if not isinstance(name, str) or not 3 <= len(name) <= 32 or not name.isascii() or not name.replace("_", "a").isalnum():
                    raise BadRequest(400, "用户名须为 3—32 位字母、数字或下划线")
                if not isinstance(password, str) or not 6 <= len(password) <= 128:
                    raise BadRequest(400, "密码长度须为 6—128 位")
                row = db.execute("SELECT username,salt,digest FROM accounts WHERE username=?", (name,)).fetchone()
                if path == "/api/accounts/register":
                    if row:
                        raise BadRequest(409, "该用户名已存在")
                    salt = secrets.token_bytes(16)
                    try:
                        db.execute("INSERT INTO accounts(username,salt,digest,native_digest) VALUES(?,?,?,?)",
                            (name, salt, password_digest(password, salt), native_digest_from_password(password, salt)))
                    except sqlite3.IntegrityError:
                        raise BadRequest(409, "该用户名已存在")
                else:
                    if not row or not hmac.compare_digest(row[2], password_digest(password, row[1])):
                        raise BadRequest(401, "用户名或密码错误")
                    name = row[0]
                    db.execute("UPDATE accounts SET native_digest=? WHERE username=?",
                        (native_digest_from_password(password, row[1]),name))
                token = secrets.token_urlsafe(32)
                db.execute("INSERT INTO sessions VALUES(?,?,?)", (hashlib.sha256(token.encode()).hexdigest(), name, int(time.time()) + 86400))
                db.commit()
                return self.reply(201 if path.endswith("register") else 200, {"username": name, "token": token})
            if path == "/api/session" and method == "DELETE":
                _, digest = self.current_user(db)
                db.execute("DELETE FROM sessions WHERE digest=?", (digest,))
                db.commit()
                return self.reply(200, {"status": "logged-out"})
            if path == "/api/save" and method in ("GET", "PUT"):
                name, _ = self.current_user(db)
                if method == "PUT":
                    value = self.body().get("data")
                    if not isinstance(value, dict):
                        raise BadRequest(400, "存档须为 JSON 对象")
                    db.execute("INSERT INTO saves VALUES(?,?,?) ON CONFLICT(username) DO UPDATE SET data=excluded.data,updated=excluded.updated",
                               (name, json.dumps(value, ensure_ascii=False, allow_nan=False), int(time.time())))
                    db.commit()
                    return self.reply(200, {"status": "saved"})
                row = db.execute("SELECT data,updated FROM saves WHERE username=?", (name,)).fetchone()
                return self.reply(200, {"data": json.loads(row[0]) if row else {}, "updated": row[1] if row else None})
        raise BadRequest(404, "没有这个本地接口")

    def native_login(self, query):
        """Authenticate local accounts and bind only their trusted local game identity."""
        name = query.get("login", [""])[0]
        password = query.get("password", [""])[0]
        version = query.get("version", [""])[0]
        if version != "4.0.25034" or not name or not password or len(name)>32 or len(password)>128:
            return self.reply(200, '<response success="false"><error>1</error><region>CN</region></response>', 'application/xml; charset=utf-8')
        with connect(self.server.db_path) as db:
            row = db.execute("SELECT rowid,username,salt,digest,native_digest FROM accounts WHERE username=?", (name,)).fetchone()
            matches = False
            if row:
                if re.fullmatch(r"[a-fA-F0-9]{40}",password):
                    matches = row[4] is not None and hmac.compare_digest(row[4],native_password_digest(password,row[2]))
                else:
                    matches = hmac.compare_digest(row[3], password_digest(password, row[2]))
            if not matches:
                return self.reply(200, '<response success="false"><error>1</error><region>CN</region></response>', 'application/xml; charset=utf-8')
            return self.native_login_success(db, row[0], row[1])

    def native_token_login(self, query):
        """Resume only an unexpired local credential; the token determines the account."""
        token = query.get('token', [''])[0]
        if query.get('version', [''])[0] != '4.0.25034' or not re.fullmatch(r'[a-fA-F0-9]{64}', token):
            return self.reply(200, '<response success="false"><error>1</error><region>CN</region></response>', 'application/xml; charset=utf-8')
        with connect(self.server.db_path) as db:
            row = db.execute('''SELECT a.rowid,a.username FROM sessions s JOIN accounts a ON a.username=s.username
                                WHERE s.digest=? AND s.expires>?''',
                             (hashlib.sha256(token.encode()).hexdigest(), int(time.time()))).fetchone()
            if row is None:
                return self.reply(200, '<response success="false"><error>1</error><region>CN</region></response>', 'application/xml; charset=utf-8')
            return self.native_login_success(db, row[0], row[1])

    def native_login_success(self, db, rowid, username):
        user_id = local_game_uid(self.server, username, rowid)
        if user_id is None:
            return self.reply(200, '<response success="false"><error>1</error><region>CN</region></response>', 'application/xml; charset=utf-8')
        token = secrets.token_hex(32)
        expires = int(time.time()) + 86400
        db.execute("INSERT INTO sessions VALUES(?,?,?)", (hashlib.sha256(token.encode()).hexdigest(), username, expires))
        db.commit()
        xml = (f'<response success="true"><error>0</error><token>{token}</token>'
               f'<host>127.0.0.1</host><port>{self.server.game_port}</port>'
               f'<logintoken>{token}</logintoken><logintokenexpires>{expires}</logintokenexpires>'
               f'<userid>{user_id}</userid><region>CN</region></response>')
        return self.reply(200, xml, 'application/xml; charset=utf-8')

    def handle_request(self):
        self.connection.settimeout(15)
        try:
            self.dispatch()
        except (BadRequest, LocalOptionsError) as e:
            self.reply(e.status, {"error": e.message})
        except (TimeoutError, BrokenPipeError, ConnectionResetError):
            pass
        except (ValueError, sqlite3.Error):
            self.reply(500, {"error": "请求未能保存，请检查数据格式或本地数据库"})

    do_GET = do_POST = do_PUT = do_DELETE = do_OPTIONS = handle_request


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=19214)
    parser.add_argument("--game-port", type=int, default=19213)
    parser.add_argument("--data-dir", type=Path, default=Path(__file__).resolve().parent / "data")
    parser.add_argument("--open-browser", action="store_true")
    backend_options = parser.add_mutually_exclusive_group()
    backend_options.add_argument("--backend-address", help="Authenticated loopback game logic endpoint")
    backend_options.add_argument("--managed-backend", action="store_true")
    args = parser.parse_args()
    try:
        server = Server(("127.0.0.1", args.port), Handler)
    except OSError:
        # A second launch may reuse only this prototype, never an unrelated service.
        import urllib.request
        url = f"http://127.0.0.1:{args.port}"
        try:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(url + "/api/health", timeout=2) as response:
                found = json.load(response)
            if found.get("app") != APP_ID:
                raise RuntimeError("端口被其他程序占用")
        except Exception:
            raise SystemExit("端口被其他程序占用，请使用 --port 指定其他端口")
        if args.open_browser:
            webbrowser.open(url)
        return
    server.db_path = args.data_dir.resolve() / "local.sqlite3"
    server.control = secrets.token_urlsafe(32)
    server.client_events = deque(maxlen=100)
    server.events_lock = threading.Lock()
    game_server = managed_backend = None
    try:
        initialize(server.db_path)
        server.catalog = load_catalog(args.data_dir.resolve() / 'catalog')
        source_ids = {}
        server.character_inventory = load_character_inventory(args.data_dir.resolve() / 'characters', source_ids)
        server.character_details = load_character_details(args.data_dir.resolve() / 'characters', server.character_inventory)
        server.character_profiles = load_character_profiles(args.data_dir.resolve() / 'characters', source_ids)
        server.character_user_ids = {name: source_ids[name] for name in server.character_profiles}
        server.character_bootstrap = load_character_bootstrap(args.data_dir.resolve() / 'characters', server.character_profiles)
        server.character_mailbox = load_character_mailbox(args.data_dir.resolve() / 'characters', server.character_profiles)
        server.backend_config = None
        if args.managed_backend:
            from rf4_service_process import BackendProcess
            managed_backend = BackendProcess(args.data_dir.resolve())
            managed_backend.start()
            server.backend_process = managed_backend
            server.backend_config = (managed_backend.address, managed_backend.psk)
        elif args.backend_address:
            host, _, port = args.backend_address.rpartition(':')
            psk = os.environ.get('RF4_BACKEND_PSK', '')
            if host != '127.0.0.1' or not port.isdecimal() or not 1 <= int(port) <= 65535 or len(psk.encode()) < 32:
                raise RuntimeError('本地业务服务地址或密钥无效')
            server.backend_config = ((host, int(port)), psk)
        game_server = GameServer(("127.0.0.1", args.game_port), GameHandler)
        game_server.http = server
        server.game_port = game_server.server_address[1]
        threading.Thread(target=game_server.serve_forever, daemon=True).start()
        url = f"http://127.0.0.1:{server.server_port}"
        if args.open_browser:
            webbrowser.open(url)
        if sys_stdout := getattr(__import__("sys"), "stdout", None):
            print(json.dumps({"url": url, "database": str(server.db_path),
                              "gameProtocol": 'reference-experimental' if server.backend_config else 'bootstrap-only'}), flush=True)
        server.serve_forever(poll_interval=0.2)
    except KeyboardInterrupt:
        pass
    finally:
        if game_server:
            game_server.shutdown()
            game_server.server_close()
        if managed_backend:
            managed_backend.close()
        server.server_close()


if __name__ == "__main__":
    main()
