'use strict';
(()=>{
let token='',profile=null,epoch=0;
const $=id=>document.getElementById(id),auth=$('local-auth');
const money=n=>Number.isSafeInteger(n)?(n/100).toLocaleString('zh-CN',{minimumFractionDigits:2,maximumFractionDigits:2}):'未知';
async function api(path,method='GET',data){const headers={};if(token)headers.Authorization='Bearer '+token;if(data!==undefined)headers['Content-Type']='application/json';const response=await fetch(path,{method,headers,body:data===undefined?undefined:JSON.stringify(data),cache:'no-store'});const value=await response.json();if(!response.ok){const e=Error(value.error||'请求失败');e.status=response.status;throw e;}return value;}
function render(p){profile=p;$('account_name').textContent=p?p.nickname:'请登录本地账户';$('login_button').textContent=token?'退出账户':'登录本地账户';const values=p?[p.username+' / ID '+p.userId,String(p.level)+(p.stars?' · ★ '+p.stars:''),'未设置',p.region,p.premium?(p.premiumUntil?'至 '+new Date(p.premiumUntil).toLocaleString('zh-CN',{hour12:false}):'长期会员'):'未开通',money(p.goldMinor),money(p.silverMinor)]:['未登录','—','未设置','中国','—','—','—'];document.querySelectorAll('.user_info .info tbody tr').forEach((row,i)=>{const b=row.querySelector('b');if(b)b.textContent=values[i];});$('local-refresh').disabled=!token;}
async function refresh(){if(!token)return;const own=epoch;const p=await api('/api/profile');if(own===epoch)render(p);}
function requestLogin(){if(token)return;$('local-auth-message').textContent='请输入游戏使用的本地账户。';if(!auth.open)auth.showModal();}
async function authenticate(register){const form=$('local-login');if(!form.reportValidity())return;const buttons=form.querySelectorAll('button');buttons.forEach(b=>b.disabled=true);try{const d=await api(register?'/api/accounts/register':'/api/session','POST',{username:form.username.value,password:form.password.value});token=d.token;epoch++;render(null);await refresh();auth.close();$('local-message').textContent=register?'本地账户已创建。':'已登录本地角色。';}catch(e){if(token){auth.close();$('local-message').textContent=e.message;}else $('local-auth-message').textContent=e.message;}finally{form.password.value='';buttons.forEach(b=>b.disabled=false);}}
$('local-login').onsubmit=e=>{e.preventDefault();authenticate(false);};$('local-register').onclick=()=>authenticate(true);
$('login_button').onclick=async()=>{if(!token){requestLogin();return;}try{await api('/api/session','DELETE');token='';epoch++;render(null);$('local-message').textContent='已退出本地账户。';}catch(e){$('local-message').textContent=e.message;}};
$('local-auth-close').onclick=()=>auth.close();$('local-refresh').onclick=()=>refresh().catch(e=>$('local-message').textContent=e.message);
$('close-banner').onclick=()=>document.getElementById('head_banner').hidden=true;
render(null);
})();
