const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');

test('fishing rule switches save independently, reload and fit supported sizes and DPI',async()=>{
 const rules=[{key:'fish_bait_rules_enabled',prefix:'fishRules',default:'0'},{key:'fish_distribution_enabled',prefix:'fishDistribution',default:'0'},{key:'fish_weather_enabled',prefix:'fishWeather',default:'0'}];
 const values={debug_mode:'1',bite_delay_fixed:'17'};
 let failRead=false,failWrite=false,writes=0;
 const html=fs.readFileSync(path.join(__dirname,'web/index.html'));
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://fixture'),key=url.pathname.replace(/^\/admin/,'');
  const json=(value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
  if(key==='/api/me')return json({username:'admin'});
  if(key==='/api/fish/areas')return json([]);
  if(key==='/api/tunables'){
   if(req.method==='GET')return failRead?json({error:'读取失败'},503):json(values);
   let body='';req.on('data',c=>body+=c);req.on('end',()=>{
    if(failWrite)return json({error:'保存失败'},503);
    const value=JSON.parse(body);assert.ok(rules.some(x=>x.key===value.key));assert.ok(['0','1'].includes(value.value));
    values[value.key]=value.value;writes++;json({ok:true});
   });return;
  }
  if(key.startsWith('/api/'))return json({});
  res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html);
 });
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 let browser;
 const metrics=[],buttonMetrics=[];
 try{
  browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
  for(const width of [1280,1024,360])for(const dpi of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:dpi});
   await context.addInitScript(()=>{localStorage.setItem('rf4_local_admin_token','fixture');localStorage.setItem('rf4_admin_token','fixture');});
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   for(const rule of rules)delete values[rule.key];
   await page.goto(`http://127.0.0.1:${server.address().port}/admin/#fish`,{waitUntil:'networkidle'});
   await page.locator('#fishRulesToggle').waitFor();
   assert.equal(await page.locator('#fishRulesCard button').count(),4);
   for(const rule of rules){
    assert.equal(await page.locator('#'+rule.prefix+'Toggle').getAttribute('aria-pressed'),String(rule.default==='1'));
    assert.equal(values[rule.key],undefined,'reading default values must not save a switch');
    values[rule.key]='0';
   }
   await page.reload({waitUntil:'networkidle'});
   for(const state of ['1','0']){
    for(const rule of rules){
     const before={...values},beforeWrites=writes,button=page.locator('#'+rule.prefix+'Toggle');
     assert.equal(await button.textContent(),state==='1'?'开启':'关闭');
     await button.click();await page.waitForFunction(({prefix,state})=>document.getElementById(prefix+'Toggle').getAttribute('aria-pressed')===String(state==='1'),{prefix:rule.prefix,state});
     assert.equal(writes,beforeWrites+1);assert.deepEqual(values,{...before,[rule.key]:state});
    }
    await page.reload({waitUntil:'networkidle'});
    for(const rule of rules)assert.equal(await page.locator('#'+rule.prefix+'Toggle').textContent(),state==='1'?'关闭':'开启');
   }
   await page.locator('#fishWeatherToggle').click();await page.waitForFunction(()=>document.getElementById('fishWeatherToggle').getAttribute('aria-pressed')==='true');
   assert.deepEqual(values,{debug_mode:'1',bite_delay_fixed:'17',fish_bait_rules_enabled:'0',fish_distribution_enabled:'0',fish_weather_enabled:'1'});
   await page.reload({waitUntil:'networkidle'});assert.equal(await page.locator('#fishWeatherToggle').textContent(),'关闭');
   for(const rule of rules)values[rule.key]=rule.default;
   values.fish_weather_enabled='1';await page.reload({waitUntil:'networkidle'});
   await page.locator('.toast').evaluate(e=>e.replaceChildren());
   for(const [name,text] of [['normal',null],['chinese','尚未识别的最长合理业务饵料名称与前置条件'.repeat(12)],['continuous','UnrecognizedBait'.repeat(30)]]){
    if(text)for(const rule of rules)await page.locator('#'+rule.prefix+'Message').evaluate((e,text)=>e.textContent=text,text);
    const m=await page.locator('#fishRulesCard').evaluate(card=>{
     const rect=e=>{const r=e.getBoundingClientRect();return{x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height};};
     const rows=[...card.querySelectorAll('h2,small,p,button')].map(e=>{
      const range=document.createRange();range.selectNodeContents(e);
      return{tag:e.tagName,box:rect(e),font:getComputedStyle(e).fontSize,scroll:e.scrollWidth,client:e.clientWidth,textRects:[...range.getClientRects()].map(r=>({x:r.x,right:r.right,y:r.y,bottom:r.bottom}))};
     });
     return{viewport:innerWidth,document:document.documentElement.scrollWidth,card:rect(card),rows};
    });
    assert.ok(m.document<=width+2,JSON.stringify({width,dpi,name,m}));
    for(const r of m.rows){
     assert.ok(r.scroll<=r.client+2,JSON.stringify({width,dpi,name,r}));
     assert.ok(r.box.x>=m.card.x&&r.box.right<=m.card.right+1);
     for(const t of r.textRects)assert.ok(t.x>=r.box.x-1&&t.right<=r.box.right+1&&t.y>=r.box.y-1&&t.bottom<=r.box.bottom+1);
    }
    const title=m.rows.find(r=>r.tag==='H2').box,button=m.rows.find(r=>r.tag==='BUTTON').box;
    const pairs=await page.locator('#fishRulesCard .rule-row').evaluateAll(rows=>rows.map(row=>{const d=row.querySelector('div').getBoundingClientRect(),b=row.querySelector('button').getBoundingClientRect();return{right:d.right,bottom:d.bottom,x:b.x,y:b.y};}));
    for(const p of pairs)assert.ok(p.x>=p.right||p.y>=p.bottom-1,'description and button overlap');
    assert.ok(button.x>=title.right||button.y>=title.bottom-1,'title and button overlap');
    metrics.push({width,dpi,name,cardWidth:m.card.width,cardHeight:m.card.height});
    for(const rule of rules){
     const target=page.locator('#'+rule.prefix+'Toggle');await target.click({trial:true});
     const b=await target.evaluate(e=>{const r=e.getBoundingClientRect();const hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return{width:r.width,height:r.height,font:getComputedStyle(e).fontSize,y:r.y,bottom:r.bottom,viewport:innerHeight,reachable:hit===e||e.contains(hit)};});
     assert.ok(b.y>=-1&&b.bottom<=b.viewport+1&&b.reachable,JSON.stringify({width,dpi,name,prefix:rule.prefix,b}));
     buttonMetrics.push({prefix:rule.prefix,width:b.width,height:b.height,font:b.font});
    }
    if(process.env.RF4_RULES_SCREENSHOT_DIR&&dpi===1.5&&name==='normal'&&width!==1024)await page.screenshot({path:path.join(process.env.RF4_RULES_SCREENSHOT_DIR,`rules-${width}.png`),fullPage:true});
   }
   for(const rule of rules){
    const before={...values};failWrite=true;await page.locator('#'+rule.prefix+'Toggle').click();await page.waitForFunction(prefix=>!document.getElementById(prefix+'Toggle').disabled,rule.prefix);assert.deepEqual(values,before);failWrite=false;
    const state=values[rule.key]==='1'?'0':'1';await page.locator('#'+rule.prefix+'Toggle').click();await page.waitForFunction(({prefix,state})=>document.getElementById(prefix+'Toggle').getAttribute('aria-pressed')===String(state==='1'),{prefix:rule.prefix,state});assert.deepEqual(values,{...before,[rule.key]:state});
   }
   failRead=true;await page.reload({waitUntil:'networkidle'});for(const rule of rules){assert.equal(await page.locator('#'+rule.prefix+'Toggle').isDisabled(),true);assert.equal(await page.locator('#'+rule.prefix+'Badge').textContent(),'读取失败');}failRead=false;
   assert.deepEqual(errors,[]);await context.close();
  }
  console.log(JSON.stringify({renderedCases:metrics.length,measurements:metrics,scrollAndActionChecks:buttonMetrics.length,buttons:rules.map(rule=>{const rows=buttonMetrics.filter(x=>x.prefix===rule.prefix);return{prefix:rule.prefix,minWidth:Math.min(...rows.map(x=>x.width)),maxWidth:Math.max(...rows.map(x=>x.width)),minHeight:Math.min(...rows.map(x=>x.height)),maxHeight:Math.max(...rows.map(x=>x.height)),fonts:[...new Set(rows.map(x=>x.font))]};})}));
 }finally{if(browser)await browser.close();await new Promise(r=>server.close(r));}
});
