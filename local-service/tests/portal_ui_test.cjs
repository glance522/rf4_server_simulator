const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');
const root=path.resolve(__dirname,'../..');
test('local portal fits supported widths, DPI and long account text',async()=>{
 let text='测试角色';
 const json=(res,value)=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify(value));};
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://fixture');
  if(url.pathname==='/api/session')return json(res,{token:'fixture',username:'fixture_user'});
  if(url.pathname==='/api/profile')return json(res,{nickname:text,username:text,userId:42,level:20,stars:1,region:'中国',goldMinor:2000,silverMinor:12345,premium:true,premiumUntil:null});
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
  console.log(JSON.stringify({renderedCases:cases,measurements}));
 }finally{await browser.close();await new Promise(resolve=>server.close(resolve));}
});
