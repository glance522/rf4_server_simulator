const {test}=require('node:test'),assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');
test('store summary and controls fit supported widths, DPI and long identifiers',async()=>{
 const html=fs.readFileSync(path.resolve(__dirname,'web/index.html'),'utf8');
 let store='fishingstore_009';
 const server=http.createServer((req,res)=>{
  const pathname=new URL(req.url,'http://fixture').pathname.replace(/^\/admin\/api\//,'/api/');
  const json=value=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value));};
  if(pathname==='/api/me')return json({username:'admin'});
  if(pathname==='/api/stores')return json([{store_id:store,item_count:1,override_count:0}]);
  if(pathname==='/api/stores/items')return json([{store_id:store,item_id:'bait_001',name:'测试商品',category:2,base_silver:100,base_gold:1,base_stock:0,silver:100,gold:1,stock:1000,has_override:false}]);
  if(pathname.startsWith('/api/'))return json({});
  res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html);
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
 let cases=0;
 try{
  for(const width of [1920,1024,360])for(const scale of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:scale});
   await context.addInitScript(()=>{localStorage.setItem('rf4_admin_token','fixture');localStorage.setItem('rf4_local_admin_token','fixture');});
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   for(const value of ['fishingstore_009','最长合理商店标识中文'.repeat(4),'Store'.repeat(32)]){
    store=value;await page.goto('http://127.0.0.1:'+server.address().port+'/admin/',{waitUntil:'networkidle'});
    await page.evaluate(()=>go('stores'));await page.waitForFunction(()=>document.querySelector('#storeSummary').textContent.includes('补至 1000'));
    const m=await page.evaluate(()=>{const e=document.querySelector('#storeSummary'),r=e.getBoundingClientRect();return{font:getComputedStyle(e).fontSize,width:r.width,height:r.height,scroll:e.scrollWidth,available:e.clientWidth,page:document.documentElement.scrollWidth,viewport:innerWidth};});
    assert.ok(m.page<=m.viewport+2&&m.scroll<=m.available+1,JSON.stringify({width,scale,value,m}));
    for(const input of await page.locator('#storeTable input').all()){await input.scrollIntoViewIfNeeded();const r=await input.boundingBox();assert.ok(r.x>=0&&r.x+r.width<=width+2,JSON.stringify(r));}
    assert.deepEqual(errors,[]);if(scale===1&&value==='fishingstore_009')console.log(JSON.stringify({width,scale,...m}));cases++;
   }
   await context.close();
  }
  console.log(JSON.stringify({storeRenderCases:cases,widths:[1920,1024,360],dpi:[1,1.25,1.5],textCases:3}));
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
});
