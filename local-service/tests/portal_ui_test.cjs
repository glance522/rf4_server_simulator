const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');
const root=path.resolve(__dirname,'../..');
test('local portal fits supported widths, DPI and long account text',async()=>{
 let text='测试角色',mode='',profileFailures=0,posts=[],charges=0;const receipts=new Map();
 const member=()=>({membership:{active:charges>0,permanent:false,expiresAt:charges?2000000000:0,xpBonusRate:charges?1:0},balance:{gold:charges?0:2000,silver:12345},plans:[1,7,30].map(days=>({id:'premium_'+days+'d',name:text+' '+days+' 天',days,priceGold:100}))});
 const json=(res,value)=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value));};
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://fixture');
  if(url.pathname==='/api/session')return json(res,{token:'fixture',username:'fixture_user'});
  if(url.pathname==='/api/membership'){if(mode==='status-unavailable'){res.statusCode=503;return json(res,{error:'会员工具未安装'});}return json(res,member());}
  if(url.pathname==='/api/membership/purchase'){let body='';req.on('data',part=>body+=part);req.on('end',()=>{const value=JSON.parse(body);posts.push(value);const replayed=receipts.has(value.requestID);if(!replayed){receipts.set(value.requestID,true);charges++;}if(mode==='lost-response')return req.socket.destroy();if(mode==='refresh-failure'){mode='';profileFailures=1;}json(res,{...member(),purchase:{...value,replayed}});});return;}
  if(url.pathname==='/api/profile'){if(profileFailures-- >0){res.statusCode=503;return json(res,{error:'fixture refresh unavailable'});}return json(res,{nickname:text,username:text,userId:42,level:20,stars:1,region:'中国',goldMinor:member().balance.gold,silverMinor:12345,premium:charges>0,premiumUntil:charges?2000000000000:null});}
  const name=url.pathname.slice('/portal/'.length)||'index.html';
  const file=path.join(root,['framework.css','portal.css','reference-ui.css','local.css'].includes(name)?'internal/portal/web':'local-service/web/portal',name);
  if(!fs.existsSync(file)){res.statusCode=404;return res.end();}
  res.setHeader('Content-Type',file.endsWith('.html')?'text/html; charset=utf-8':file.endsWith('.js')?'application/javascript':'text/css');res.end(fs.readFileSync(file));
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const base='http://127.0.0.1:'+server.address().port;
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 let cases=0,measurements=[];
 try{
  for(const width of [1280,1024,360])for(const scale of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:scale});
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   for(const value of ['测试角色','最长合理业务昵称中文'.repeat(4),'Player'.repeat(24)]){
    text=value;await page.goto(base+'/portal/',{waitUntil:'networkidle'});
    await page.locator('#login_button').click();
    await page.locator('[name=username]').fill('fixture_user');await page.locator('[name=password]').fill('fixture-password');
    const modal=await page.locator('#local-auth').evaluate(e=>{const r=e.getBoundingClientRect();return{x:r.x,right:r.right,width:r.width,scroll:e.scrollWidth,client:e.clientWidth};});
    assert.ok(modal.x>=0&&modal.right<=width+1&&modal.scroll<=modal.client+1,JSON.stringify({width,scale,modal}));
    await page.locator('#local-login button[type=submit]').click();await page.waitForFunction(value=>document.getElementById('account_name').textContent===value,value);
    await page.locator('.local-plan button').first().waitFor();assert.equal(await page.locator('#local-member-message').textContent(),'');
    const measured=await page.evaluate(()=>{
     const visible=e=>e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden';
     const controls=[...document.querySelectorAll('button,input,#login_button')].filter(visible).map(e=>{const r=e.getBoundingClientRect();return{id:e.id,x:r.x,right:r.right,width:r.width,height:r.height,font:getComputedStyle(e).fontSize,clipped:e.scrollWidth>e.clientWidth+2};});
     const info=document.querySelector('.info').getBoundingClientRect(),name=document.querySelector('#account_name').getBoundingClientRect();
     return{viewport:innerWidth,documentWidth:document.documentElement.scrollWidth,controls,info:{width:info.width,height:info.height},name:{width:name.width,height:name.height}};
    });
    assert.ok(measured.documentWidth<=width+2,JSON.stringify({width,scale,value,measured}));
    assert.deepEqual(measured.controls.filter(e=>e.x< -1||e.right>width+2||e.clipped||e.width<15||e.height<(e.id==='login_button'?12:15)),[],JSON.stringify({width,scale,value,measured}));
    assert.deepEqual(errors,[]);if(value==='测试角色')measurements.push({width,scale,...measured});cases++;
   }
   await context.close();
  }
  for(const failureMode of ['refresh-failure','lost-response']){
   mode=failureMode;charges=0;posts=[];receipts.clear();text='测试角色';
   const context=await browser.newContext({viewport:{width:360,height:900}}),page=await context.newPage();
   const login=async()=>{await page.locator('#login_button').click();await page.locator('[name=username]').fill('fixture_user');await page.locator('[name=password]').fill('fixture-password');await page.locator('#local-login button[type=submit]').click();await page.locator('.local-plan button').first().waitFor();};
   await page.goto(base+'/portal/');await login();await page.locator('.local-plan button').first().click();
   try{await page.waitForFunction(()=>document.querySelector('#local-member-message').textContent.includes('重试本次购买'),null,{timeout:5000});}catch(e){console.log(JSON.stringify({failureMode,charges,posts,body:await page.locator('body').innerText()}));throw e;}
   assert.equal(charges,1);const original=posts[0].requestID;mode='';
   await page.reload();await login();const retry=page.getByRole('button',{name:'重试本次购买'});await retry.waitFor();assert.equal(await retry.isEnabled(),true);
   await retry.click();await page.waitForFunction(()=>document.querySelector('#local-member-message').textContent.includes('未重复扣款'));
   assert.ok(posts.length>=2);assert.ok(posts.every(p=>p.requestID===original));assert.equal(charges,1);await context.close();
  }
  mode='status-unavailable';text='资料仍可读取';
  {const context=await browser.newContext({viewport:{width:360,height:900}}),page=await context.newPage();await page.goto(base+'/portal/');await page.locator('#login_button').click();await page.locator('[name=username]').fill('fixture_user');await page.locator('[name=password]').fill('fixture-password');await page.locator('#local-login button[type=submit]').click();await page.waitForFunction(()=>document.getElementById('local-member-message').textContent.includes('会员工具未安装'));assert.equal(await page.locator('#account_name').textContent(),text);assert.equal(await page.locator('#local-refresh').isEnabled(),true);await context.close();}
  console.log(JSON.stringify({renderedCases:cases,retryModes:['refresh-failure','lost-response'],measurements}));
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
});
