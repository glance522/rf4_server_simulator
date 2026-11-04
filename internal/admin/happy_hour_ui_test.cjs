const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');

test('happy hour switch and remaining time fit supported sizes and long text',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||undefined});
 const measurements=[];
 try{
  for(const width of [1280,1024,360])for(const dpi of [1,1.25,1.5]){
   let happy;
   const writes=[];
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:dpi});
   await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   await page.route('**/*',route=>{
    const url=new URL(route.request().url()),key=url.pathname.replace(/^\/admin/,'');
    if(url.pathname==='/admin/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
    let data=key==='/api/me'?{username:'admin'}:[];
    if(key==='/api/tunables'){
     if(route.request().method()==='POST'){
      const body=route.request().postDataJSON();writes.push(body);
      assert.equal(body.key,'happy_hour_enabled');assert.ok(['0','1'].includes(body.value));happy=body.value;data={ok:true};
     }else data={fish_bait_rules_enabled:'0',fish_distribution_enabled:'0',...(happy===undefined?{}:{happy_hour_enabled:happy})};
    }
    return route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
   });
   await page.goto('http://127.0.0.1:19214/admin/#fish');
   const toggle=page.locator('#happyHourToggle');await toggle.waitFor();
   assert.equal(await toggle.textContent(),'关闭');assert.equal(await toggle.getAttribute('aria-pressed'),'true');
   await toggle.click();await page.waitForFunction(()=>document.getElementById('happyHourToggle').getAttribute('aria-pressed')==='false');
   assert.equal(await page.locator('#fishRulesToggle').getAttribute('aria-pressed'),'false');assert.equal(await page.locator('#fishDistributionToggle').getAttribute('aria-pressed'),'false');
   await page.reload();await page.waitForFunction(()=>document.getElementById('happyHourToggle').textContent==='开启');
   await toggle.click();await page.waitForFunction(()=>document.getElementById('happyHourToggle').getAttribute('aria-pressed')==='true');
   assert.deepEqual(writes.map(x=>x.value),['0','1']);
   for(const [scenario,text,active,remaining,label] of [['normal','欢乐时光',true,3600,'生效中'],['chinese','最长合理欢乐时光状态说明'.repeat(12),false,1800,'暂停'],['continuous','UnbrokenHappyHourStatus'.repeat(25),false,0,'暂停']]){
    await page.evaluate(()=>location.hash='fish');await page.locator('#view-fish').waitFor({state:'visible'});
    await page.waitForFunction(()=>!document.getElementById('happyHourToggle').disabled);
    if(scenario!=='normal')await page.locator('#happyHourMessage').evaluate((e,text)=>e.textContent=text,text);
    const switchSize=await page.locator('#fishRulesCard').evaluate(card=>{
     const b=card.querySelector('#happyHourToggle'),d=card.querySelector('#happyHourMessage'),br=b.getBoundingClientRect(),dr=d.getBoundingClientRect();
     return{scroll:document.documentElement.scrollWidth,width:innerWidth,buttonWidth:br.width,buttonHeight:br.height,font:getComputedStyle(b).fontSize,messageScroll:d.scrollWidth,messageWidth:d.clientWidth,overlap:br.x<dr.right&&br.y<dr.bottom};
    });
    assert.ok(switchSize.scroll<=width+1&&switchSize.messageScroll<=switchSize.messageWidth+1&&!switchSize.overlap,JSON.stringify({width,dpi,scenario,switchSize}));
    await page.evaluate(()=>location.hash='players');await page.locator('#view-players').waitFor({state:'visible'});
    await page.evaluate(({active,remaining})=>{
     PLAYER={uid:1,name:'测试玩家',level:25,xp:0,item_count:0,keepnet_count:0,mail_count:0,membership:0,skills:[],happy_hour:{enabled:true,active,remaining_seconds:remaining}};
     PLAYER_TAB='profile';renderPlayerDetail();
    },{active,remaining});
    const clock=page.locator('#playerHappyHour');assert.match(await clock.textContent(),new RegExp(label));assert.match(await clock.textContent(),new RegExp('今日剩余 '+Math.ceil(remaining/60)+' 分钟'));
    if(scenario!=='normal')await clock.evaluate((e,text)=>e.append(' '+text),text);
    const clockSize=await clock.evaluate(e=>{
     const r=e.getBoundingClientRect(),range=document.createRange();range.selectNodeContents(e);
     return{width:r.width,height:r.height,font:getComputedStyle(e).fontSize,scroll:e.scrollWidth,client:e.clientWidth,box:{x:r.x,y:r.y,right:r.right,bottom:r.bottom},texts:[...range.getClientRects()].map(x=>({x:x.x,y:x.y,right:x.right,bottom:x.bottom}))};
    });
    assert.ok(clockSize.scroll<=clockSize.client+1);
    for(const t of clockSize.texts)assert.ok(t.x>=clockSize.box.x-1&&t.right<=clockSize.box.right+1&&t.y>=clockSize.box.y-1&&t.bottom<=clockSize.box.bottom+1);
    measurements.push({width,dpi,scenario,...switchSize,happyWidth:clockSize.width,happyHeight:clockSize.height,happyFont:clockSize.font});
   }
   await page.evaluate(()=>{PLAYER.happy_hour={enabled:false,active:false,remaining_seconds:1800};renderPlayerDetail()});
   assert.match(await page.locator('#playerHappyHour').textContent(),/已关闭.*今日剩余 30 分钟/);
   assert.deepEqual(errors,[]);await context.close();
  }
 }finally{await browser.close();}
 console.log(JSON.stringify({renderedCases:measurements.length,measurements}));
});
