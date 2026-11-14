const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');
test('params tabs preserve edits and upstream embedded API routing',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 const metrics=[];
 try{
  for(const width of [360,1024,1920])for(const scale of [1,1.25,1.5])for(const prefix of ['', '/admin']){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:scale});
   await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
   const page=await context.newPage(),errors=[],requests=[];page.on('pageerror',e=>errors.push(e.message));
   await page.route('**/*',route=>{
    const url=new URL(route.request().url());requests.push(url.pathname);
    if(url.pathname===prefix+'/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
    assert.ok(url.pathname.startsWith(prefix+'/api/'),url.pathname);
    const key=url.pathname.slice(prefix.length),data=key==='/api/me'?{username:'admin'}:key==='/api/tunables'?{xp_gain:'1'}:key==='/api/audit'?[]:{sessions:1,players:1,items:1,mails:0,tunables:1,audits:0};
    return route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
   });
   await page.clock.install();
   await page.goto('http://127.0.0.1:19214'+prefix+'/#params');
   await page.locator('button[data-page="gameplay"]').waitFor();
   assert.equal(await page.locator('#paramsBody .tunable-group[data-page="gameplay"]').count(),6);
   assert.equal(await page.locator('#paramsBody .tunable-group[data-page="reward"]').count(),5);
   assert.equal(await page.locator('#paramsBody .tunable-group[data-page="event"]').count(),2);
   assert.equal(await page.locator('#tun_fish_weight_cap').getAttribute('placeholder'),'未设置');
   assert.ok((await page.locator('label[for="tun_fish_weight_cap"]').locator('..').innerText()).includes('默认 0'));
   assert.equal(await page.locator('#tun_electric_locked_multiplier').count(),1);
   assert.equal(await page.locator('#tun_electric_motor_kg_per_power').count(),1);
   assert.equal(await page.locator('#tun_reel_slip_wear').isVisible(),true);
   assert.equal(await page.locator('#tun_reel_slip_deadband_ratio').isVisible(),true);
   assert.equal(await page.locator('#tun_wear_stress_oil_per_s').isVisible(),true);
   await page.locator('button[data-page="reward"]').click();
   await page.locator('#tun_xp_gain').waitFor({state:'visible'});
   await page.locator('button[data-page="event"]').click();
   await page.locator('#tun_event_branches').waitFor({state:'visible'});
   await page.locator('button[data-page="reward"]').click();
   await page.locator('#tun_xp_gain').waitFor({state:'visible'});
   await page.locator('#tun_xp_gain').fill('2.5');
   await page.clock.fastForward(8100);
   await page.waitForTimeout(100);
   assert.equal(await page.locator('#tun_xp_gain').inputValue(),'2.5');
   for(const text of ['', '最长合理业务参数名称与说明'.repeat(10), 'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.repeat(15)]){
    if(text)await page.locator('.tunable-field:visible label').first().evaluate((e,t)=>e.textContent=t,text);
    const m=await page.evaluate(()=>({viewport:innerWidth,scroll:document.documentElement.scrollWidth,fields:[...document.querySelectorAll('.tunable-field')].filter(e=>e.offsetParent!==null&&e.querySelector('input')&&e.querySelector('button')).map(e=>{
     const input=e.querySelector('input').getBoundingClientRect(),button=e.querySelector('button').getBoundingClientRect(),label=e.querySelector('label');
     return {input:input.width,button:button.width,height:button.height,gap:button.x-input.right,labelScroll:label.scrollWidth,labelWidth:label.clientWidth,font:getComputedStyle(label).fontSize};
    })}));
    assert.ok(m.scroll<=m.viewport+1,JSON.stringify({width,scale,prefix,m}));
    for(const f of m.fields){assert.ok(f.input>=50&&f.button>=40&&f.height>=28&&f.gap>=7,JSON.stringify({width,scale,prefix,f}));assert.ok(f.labelScroll<=f.labelWidth+1);}
    metrics.push({width,scale,prefix,text:!text?'normal':text.startsWith('ABC')?'continuous':'chinese',...m.fields[0]});
   }
   assert.deepEqual(errors,[]);await context.close();
  }
 }finally{await browser.close();}
 console.log(JSON.stringify({cases:metrics.length,measurements:metrics}));
});

test('grant item entry is hidden in admin UI',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 try{
  const context=await browser.newContext({viewport:{width:1280,height:900}});
  await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
  const page=await context.newPage();
  await page.route('**/*',route=>{
   const url=new URL(route.request().url());
   if(url.pathname==='/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
   const key=url.pathname;
   let data={};
   if(key==='/api/me')data={username:'admin'};
   else if(key==='/api/dashboard')data={sessions:1,players:1,items:1,mails:0,tunables:1,audits:0};
   else if(key==='/api/audit')data=[];
   else if(key==='/api/tunables')data={};
   else if(key==='/api/players')data=[];
   else if(key==='/api/players/detail')data={uid:1000001,name:'Tester',level:10,xp:0,silver:0,gold:0,bank:0,membership:0,membership_exp:0,items:[],keepnet:[],mail:[],item_count:0,keepnet_count:0,mail_count:0,banned:false};
   return route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
  });
  await page.goto('http://127.0.0.1:19214/#dashboard');
  await page.locator('#qaGrid .qa').first().waitFor();
  assert.equal(await page.evaluate(()=>eval('QUICK_ACTIONS').some(([id])=>id==='grant')),false);
  await page.evaluate(()=>{PLAYER={uid:1000001,name:'Tester',level:10,xp:0,silver:0,gold:0,bank:0,membership:0,membership_exp:0,items:[],keepnet:[],mail:[],item_count:0,keepnet_count:0,mail_count:0,banned:false};PLAYER_TAB='items';renderPlayerDetail()});
  assert.equal(await page.getByRole('button',{name:'+ 发放物品'}).count(),0);
  assert.equal((await page.locator('#playerTabBody').innerText()).includes('发放物品'),false);
 }finally{await browser.close();}
});
