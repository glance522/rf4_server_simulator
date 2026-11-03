const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),vm=require('node:vm');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.resolve(__dirname,'../../internal/admin/web/index.html'),'utf8');
new vm.Script(html.split('<script>')[1].split('</script>')[0]);
const control=fs.readFileSync(path.resolve(__dirname,'../local_server.py'),'utf8').split("PAGE = r'''")[1].split("'''")[0];
const edge=path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe');
const profile={uid:7357,name:'本地角色',level:29,xp:11677292,gold:67386,silver:2868,bank:0,membership:1,membership_active:true,membership_expires_at:2000000000,item_count:0,keepnet_count:0,mail_count:0,items:[],keepnet:[],skills:[],mails:[],skill_total:0,skill_spent:0,skill_bonus:0,available_points:0};
test('admin pages fit supported widths, scaling and long text; controls stay reachable',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:edge});
 let cases=0;
 try{
  for(const width of [1920,1024,360])for(const scale of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:scale});
   const page=await context.newPage(),errors=[];let enabled=true;
   page.on('pageerror',e=>errors.push(e.message));
   await page.addInitScript(()=>localStorage.setItem('rf4_local_admin_token','fixture-token'));
   await page.route('**/*',route=>{
    const raw=new URL(route.request().url()).pathname,p=raw.startsWith('/api/')?'/admin'+raw:raw;
    const send=d=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(d)});
    if(p==='/local-control/')return route.fulfill({status:200,contentType:'text/html; charset=utf-8',body:control});
    if(p==='/'||p==='/admin/'||p==='/admin')return route.fulfill({status:200,contentType:'text/html; charset=utf-8',body:html});
    if(p==='/admin/api/me')return send({username:'admin'});
    if(p==='/admin/api/local-capabilities')return send({accounts:raw.startsWith('/admin/'),debug:raw.startsWith('/admin/')});
    if(p==='/admin/api/dashboard')return send({sessions:1,players:1,items:1021,mails:0,tunables:12,audits:5});
    if(p==='/admin/api/debug'){
     if(route.request().method()==='PUT')enabled=route.request().postDataJSON().enabled;
     return send({enabled,biteMinSeconds:3,biteMaxSeconds:5,weightMultiplier:enabled?4:1});
    }
    if(p==='/admin/api/tunables')return send({debug_mode:enabled?'1':'0',fish_weight_scale:enabled?'4':'1',xp_gain:'1'});
    if(p==='/admin/api/players')return send([profile]);
    if(p==='/admin/api/players/detail')return send(profile);
    if(p==='/admin/api/accounts')return send([{username:'123123',uid:7357,passwordState:'已设置'}]);
    if(p==='/admin/api/audit')return send([{ts:1793694602,admin:'admin',action:'membership_update',target:'7357',detail:'type=1 expiry=1794299402'}]);
    if(p==='/admin/api/stores')return send([{store_id:'fishingstore_009',item_count:2,override_count:1}]);
    if(p==='/admin/api/stores/items')return send([{store_id:'fishingstore_009',item_id:'braid_40907',name:'HiPower Braid',category:11,base_silver:0,base_gold:1513,base_stock:0,silver:0,gold:1513,stock:1000,has_override:true}]);
    if(p==='/admin/api/fish/areas')return send([]);
    if(p.startsWith('/admin/api/'))return send({ok:true});
    return route.fulfill({status:404,body:'Not found'});
   });
   await page.goto('http://127.0.0.1:19214/admin/');
   await page.waitForFunction(()=>document.querySelector('#debugBiteValue').textContent==='3–5 秒');
   await page.waitForFunction(()=>document.querySelector('#tun_fish_weight_scale'));
   const check=async view=>{
    const m=await page.evaluate(()=>{
     const visible=e=>e.getClientRects().length&&getComputedStyle(e).visibility!=='hidden';
     const clipped=[...document.querySelectorAll('button,input,select,.tunable-field label,.debug-metrics strong')].filter(visible).filter(e=>e.tagName!=='INPUT'&&e.scrollWidth>e.clientWidth+1).map(e=>(e.id||e.textContent||e.tagName).slice(0,60));
     const pairs=[...document.querySelectorAll('.tunable-field>div')].filter(visible).map(e=>[...e.children].map(c=>{const r=c.getBoundingClientRect();return{x:r.x,right:r.right,width:r.width,height:r.height}}));
     return {width:document.documentElement.clientWidth,scroll:document.documentElement.scrollWidth,clipped,pairs};
    });
    assert.ok(m.scroll<=m.width+1,JSON.stringify({width,scale,view,...m}));
    assert.deepEqual(m.clipped,[],JSON.stringify({width,scale,view,clipped:m.clipped}));
    for(const [input,button]of m.pairs)assert.ok(button.x-input.right>=7&&input.width>=50&&button.height>=28);
    cases++;
   };
   await check('dashboard');
   await page.evaluate(async()=>{document.querySelector('#tun_xp_gain').value='2.5';await loadDashboard(false);});
   assert.equal(await page.locator('#tun_xp_gain').inputValue(),'2.5');
   const size=await page.locator('.debug-metrics strong').first().evaluate(e=>({font:getComputedStyle(e).fontSize,height:e.getBoundingClientRect().height}));
   assert.equal(size.font,'17px');
   assert.equal(await page.locator('#tun_fish_weight_scale').isDisabled(),true);
   if(width===1920&&scale===1&&process.env.RF4_UI_SCREENSHOT_TARGET)await page.screenshot({path:process.env.RF4_UI_SCREENSHOT_TARGET,fullPage:true});
   await page.evaluate(()=>toggleDebugPreset());
   await page.waitForFunction(()=>document.querySelector('#debugPresetBadge').textContent==='已关闭');
   assert.equal(await page.locator('#tun_fish_weight_scale').isDisabled(),false);
   for(const name of ['accounts','players','stores','fish','mail','audit']){
    await page.evaluate(name=>go(name),name);
    if(name==='players')await page.waitForSelector('#pf_member_kind');
    if(name==='stores'){
     await page.waitForFunction(()=>document.querySelector('#storeSummary').textContent.includes('自动补货'));
     const m=await page.locator('#storeSummary').evaluate(e=>{const r=e.getBoundingClientRect();return {font:getComputedStyle(e).fontSize,height:r.height,width:r.width,scroll:e.scrollWidth};});
     assert.equal(m.font,'14px');assert.ok(m.scroll<=m.width+1);
     if(scale===1)console.log('Store summary '+width+'px: '+JSON.stringify(m));
     if(width===1920&&scale===1&&process.env.RF4_UI_SCREENSHOT_TARGET)await page.screenshot({path:process.env.RF4_UI_SCREENSHOT_TARGET,fullPage:true});
     await page.evaluate(()=>{STORE_ITEMS[0].name='长中文商品名称'.repeat(10);STORE_ITEMS[0].item_id='ABCDEFGHIJKLMNOP'.repeat(20);renderStoreTable();});
     await page.locator('#storeTable button').last().scrollIntoViewIfNeeded();
    }
    if(name==='accounts')await page.waitForFunction(()=>document.querySelector('#accountsTable').textContent.includes('123123'));
    await check(name);
   }
   await page.evaluate(()=>{go('accounts');document.querySelector('#accountsTable').textContent='长中文昵称'.repeat(25)+'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.repeat(12);});
   await check('long-text');
   await page.evaluate(()=>{go('players');PLAYER.name='长中文昵称'.repeat(8)+'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.repeat(3);renderPlayerDetail();});
   await check('long-player-name');
   assert.equal(await page.locator('#pf_name').inputValue(),'长中文昵称'.repeat(8)+'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.repeat(3));
   await page.goto('http://127.0.0.1:19214/local-control/');
   await check('service-control-links');
   await page.goto('http://127.0.0.1:19214/');
   await page.waitForFunction(()=>document.querySelector('#tun_xp_gain'));
   await check('standalone-admin-compatibility');
   assert.equal(await page.locator('.debug-card').isVisible(),false);
   assert.deepEqual(errors,[]);
   await context.close();
  }
 }finally{await browser.close();}
 console.log('Measured '+cases+' page/text cases at widths 1920/1024/360 and DPR 1/1.25/1.5; inputs and buttons separated by at least 7px.');
});
