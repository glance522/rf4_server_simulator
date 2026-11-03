const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');
const root=path.resolve(__dirname,'../..');
test('portal and membership editor fit supported widths, DPI and long names',async()=>{
 let name='测试角色',buys=0,expired=false,saved;
 const profile=()=>({uid:42,name,level:20,xp:1000000,gold:2000,silver:12345,bank:0,membership:expired?1:2,membership_exp:expired?1700000000:0,membership_expires_at:expired?1700000000:0,membership_active:!expired,stars:0,item_count:0,keepnet_count:0,mail_count:0,items:[],keepnet:[],skills:[],mails:[]});
 const member=()=>({membership:{active:false,permanent:false,expiresAt:0},balance:{gold:2000,silver:12345},plans:[{id:'premium_1d',name:'高级会员 1 天',priceGold:100}]});
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://fixture');
  const apiPath=url.pathname.startsWith('/admin/api/')?url.pathname.slice('/admin'.length):url.pathname;
  const json=value=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value));};
  if(url.pathname.startsWith('/portal/api/')){
   const key=url.pathname.slice('/portal/api/'.length);
   if(key==='session')return json({token:'fixture'});
   if(key==='profile')return json({nickname:name,username:name,userId:42,level:20,region:'中国',goldMinor:2000,silverMinor:12345});
   if(key==='membership/purchase'){buys++;return json({...member(),purchase:{replayed:false}});}
   return json(member());
  }
  if(apiPath.startsWith('/api/')){
   if(apiPath==='/api/players/update'){let body='';req.on('data',chunk=>body+=chunk);req.on('end',()=>{saved=JSON.parse(body);json({ok:true});});return;}
   if(apiPath==='/api/me')return json({username:'admin'});
   if(apiPath==='/api/players')return json([profile()]);
   if(apiPath==='/api/players/detail')return json(profile());
   return json({});
  }
  let file;
  if(url.pathname==='/admin/')file=path.join(root,'internal/admin/web/index.html');
  else file=path.join(__dirname,'web',url.pathname.slice('/portal/'.length)||'index.html');
  if(!fs.existsSync(file)){res.statusCode=404;return res.end();}
  res.setHeader('Content-Type',file.endsWith('.html')?'text/html; charset=utf-8':file.endsWith('.js')?'application/javascript':file.endsWith('.css')?'text/css':'image/png');res.end(fs.readFileSync(file));
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const base='http://127.0.0.1:'+server.address().port;
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 let count=0;
 try{
  for(const width of [1280,1024,360])for(const scale of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:scale});
   await context.addInitScript(()=>{localStorage.setItem('rf4_admin_token','fixture');localStorage.setItem('rf4_local_admin_token','fixture');});
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   for(const text of ['测试角色','最长合理业务昵称中文'.repeat(4),'Player'.repeat(24)]){
    name=text;
    for(const view of ['portal','admin']){
     await page.goto(base+'/'+view+'/',{waitUntil:'networkidle'});
     if(view==='portal'){
      await page.locator('#login_button').click();await page.locator('#local-login button[type=submit]').click();
      await page.waitForFunction(text=>document.getElementById('account_name').textContent===text,text);
      await page.locator('.local-plan button').first().waitFor();
      const before=buys;await page.locator('.local-plan button').first().click();
      await page.waitForFunction(()=>document.getElementById('local-member-message').textContent.includes('购买成功'));assert.equal(buys,before+1);
     }else{
      await page.evaluate(()=>go('players'));await page.waitForFunction(()=>document.querySelector('.player-row'));
      await page.evaluate(()=>selectPlayer(42));await page.locator('#pf_membership,#pf_member_kind').waitFor();assert.equal(await page.locator('#pf_membership,#pf_member_kind').inputValue(),'2');
     }
     const measure=await page.evaluate(()=>{
      const visible=e=>e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden';
      const controls=[...document.querySelectorAll('button,select,input,#login_button')].filter(visible).map(e=>{const r=e.getBoundingClientRect();return{id:e.id||e.textContent.slice(0,20),x:r.x,right:r.right,width:r.width,height:r.height,textLink:e.id==='login_button',scrollable:!!e.closest('.nav'),clipped:e.tagName==='BUTTON'&&e.scrollWidth>e.clientWidth+2};});
      return{width:innerWidth,documentWidth:document.documentElement.scrollWidth,controls};
     });
     assert.ok(measure.documentWidth<=measure.width+2,JSON.stringify({view,width,scale,text,measure}));
     assert.deepEqual(measure.controls.filter(c=>(!c.scrollable&&(c.x< -1||c.right>measure.width+2))||c.width<15||c.height<(c.textLink?12:15)||c.clipped),[],JSON.stringify({view,width,scale,text,measure}));
     if(view==='admin'){const last=page.locator('.nav button').last();await last.scrollIntoViewIfNeeded();const r=await last.boundingBox();assert.ok(r.x>=0&&r.x+r.width<=width+2);}
     assert.deepEqual(errors,[]);
     if(process.env.RF4_UI_SCREENSHOT_DIR&&width===360&&scale===1.5&&text.startsWith('最长'))await page.screenshot({path:path.join(process.env.RF4_UI_SCREENSHOT_DIR,view+'.png'),fullPage:true});
     count++;
    }
   }
   await context.close();
  }
  const context=await browser.newContext({viewport:{width:1280,height:900}});
  await context.addInitScript(()=>{localStorage.setItem('rf4_admin_token','fixture');localStorage.setItem('rf4_local_admin_token','fixture');});
  const page=await context.newPage();expired=true;name='测试角色';
  await page.goto(base+'/admin/');await page.evaluate(()=>go('players'));await page.waitForFunction(()=>document.querySelector('.player-row'));
  await page.evaluate(()=>selectPlayer(42));await page.locator('#pf_membership,#pf_member_kind').waitFor();
  await page.locator('#pf_gold').fill('1900');await page.evaluate(()=>saveProfile());
  assert.equal(saved.gold,1900);assert.equal('membership' in saved,false);assert.equal('membership_exp' in saved,false);
  await context.close();
  console.log(JSON.stringify({renderedCases:count,widths:[1280,1024,360],dpi:[1,1.25,1.5],textCases:3,expiredMemberProfileSave:'passed'}));
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
});
