const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const root=path.resolve(__dirname,'../web/portal');
const edge=path.join(process.env['ProgramFiles(x86)']||'', 'Microsoft/Edge/Application/msedge.exe');
async function scenario(options, run){
 const executablePath=process.env.RF4_BROWSER_EXECUTABLE||(fs.existsSync(edge)?edge:undefined);
 const browser=await chromium.launch({headless:true,executablePath});
 const state={committed:false,posts:[],charges:0};
 const member=()=>({membership:{active:state.committed,permanent:false,expiresAt:state.committed?2000000000:0},balance:{gold:67886-(state.committed?100:0),silver:2868},plans:[{id:'premium_1d',name:'高级会员1天',priceGold:100}]});
 const profile=()=>({username:'123123',userId:7357,nickname:'本地角色',level:29,region:'中国',premium:state.committed,premiumUntil:state.committed?2000000000000:null,goldMinor:member().balance.gold,silverMinor:2868});
 try{
  const page=await browser.newPage({viewport:{width:options.width||1024,height:768}});
  await page.route('**/*',async route=>{
   const url=new URL(route.request().url()),p=url.pathname;
   const json=(body,status=200)=>route.fulfill({status,contentType:'application/json',body:JSON.stringify(body)});
   if(p==='/api/session')return json({token:'fixture'});
   if(p==='/api/debug')return json({enabled:true});
   if(p==='/api/membership/purchase'){
    const body=route.request().postDataJSON();state.posts.push(body);
    if(!state.committed){state.committed=true;state.charges++;}
    if(options.uncertain&&state.posts.length===1)return json({error:'连接中断，请重试'},503);
    return json({...member(),purchase:{replayed:state.posts.length>1}});
   }
   if(p==='/api/profile'||p==='/api/membership'){
    if(options.refreshFails&&state.committed)return json({error:'状态暂不可用'},503);
    return json(p==='/api/profile'?profile():member());
   }
   if(p.startsWith('/api/'))return json({});
   const file=path.join(root,p==='/portal/'?'index.html':p.replace(/^\/portal\//,''));
   return route.fulfill({contentType:{'.html':'text/html','.css':'text/css','.js':'application/javascript','.png':'image/png','.jpg':'image/jpeg'}[path.extname(file)],body:fs.readFileSync(file)});
  });
  await page.goto('http://127.0.0.1:19214/portal/');
  await page.evaluate(()=>{const f=document.getElementById('local-login');f.password.value='123123';f.requestSubmit();});
  await page.waitForFunction(()=>document.getElementById('account_name').textContent==='本地角色');
  await run(page,state);
 }finally{await browser.close();}
}
test('confirmed purchase remains saved when account refresh fails',()=>scenario({refreshFails:true,width:360},async(page,state)=>{
 await page.evaluate(()=>document.querySelector('.local-plan button').click());
 await page.waitForFunction(()=>!document.querySelector('.local-plan button').disabled);
 assert.match(await page.locator('#local-member-message').textContent(),/购买已保存/);
 assert.match(await page.locator('#account_gold_score').textContent(),/677\.86/);
 assert.equal(await page.locator('.local-plan button').textContent(),'购买并续期');
 assert.equal(state.charges,1);
 assert.equal(await page.locator('#local-member-message').evaluate(e=>e.scrollWidth>e.clientWidth),false);
}));
test('uncertain purchase retries the same order and charges once',()=>scenario({uncertain:true},async(page,state)=>{
 await page.evaluate(()=>document.querySelector('.local-plan button').click());
 await page.waitForFunction(()=>document.querySelector('.local-plan button').textContent==='重试本次购买'&&!document.querySelector('.local-plan button').disabled);
 await page.evaluate(()=>document.querySelector('.local-plan button').click());
 await page.waitForFunction(()=>document.getElementById('local-member-message').textContent.includes('未重复扣款'));
 assert.equal(state.posts.length,2);assert.deepEqual(state.posts[0],state.posts[1]);assert.equal(state.charges,1);
 assert.match(await page.locator('#account_gold_score').textContent(),/677\.86/);
}));
