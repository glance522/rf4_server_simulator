const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');

const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');

test('event stage gifts picker follows selected stage and saves structured JSON',async()=>{
  const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
  try{
    for(const width of [1280,360]){
      const values={event_newyear_enabled:'1',event_stage_gifts:'{"13":{"1":[{"item":"fcb_coffee_can2","qty":1},{"item":"ab_tea_can2","qty":2}]}}'};
      const writes=[];
      const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:1.5});
      await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
      const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
      await page.route('**/*',route=>{
        const url=new URL(route.request().url());
        if(url.pathname==='/admin/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
        if(url.pathname==='/admin/api/me')return route.fulfill({contentType:'application/json',body:JSON.stringify({username:'admin'})});
        if(url.pathname==='/admin/api/tunables'){
          if(route.request().method()==='POST'){
            const value=JSON.parse(route.request().postData());writes.push(value);values[value.key]=value.value;
            return route.fulfill({contentType:'application/json',body:JSON.stringify({ok:true})});
          }
          return route.fulfill({contentType:'application/json',body:JSON.stringify(values)});
        }
        if(url.pathname==='/admin/api/catalog/items'){
          const q=(url.searchParams.get('q')||'').toLowerCase();
          const items=[{item_id:'fcb_coffee_can2',name:'咖啡',kind:'food',group:'食物',category:27},{item_id:'ab_tea_can2',name:'早餐茶',kind:'food',group:'食物',category:27},{item_id:'fcb_cocoa_can2',name:'可可',kind:'food',group:'食物',category:27},{item_id:'rod_blank_test',name:'竿坯组件',kind:'rod_blank',group:'组件',category:36}].filter(x=>!q||x.item_id.includes(q)||x.name.includes(q));
          const complete=url.searchParams.get('complete_only')==='1';
          const group=url.searchParams.get('group');
          const visible=(complete?items.filter(x=>x.kind!=='rod_blank'):items).filter(x=>!group||group==='food');
          return route.fulfill({contentType:'application/json',body:JSON.stringify(visible)});
        }
        if(url.pathname==='/admin/api/catalog/groups'){
          return route.fulfill({contentType:'application/json',body:JSON.stringify([{key:'food',name:'食物与饮料',count:3}])});
        }
        if(url.pathname.startsWith('/admin/api/'))return route.fulfill({contentType:'application/json',body:JSON.stringify([])});
        return route.fulfill({status:404,body:'not found'});
      });
      await page.goto(`http://127.0.0.1:19214/admin/#params`,{waitUntil:'networkidle'});
      await page.locator('button[data-page="event"]').click();
      await page.locator('#eventStageGiftPlans').waitFor();
      assert.equal(await page.locator('.event-gift-item').count(),2);
      await page.waitForFunction(()=>document.querySelector('.event-gift-item .picked')?.innerText.includes('咖啡'));
      assert.match(await page.locator('.event-gift-item .picked').first().innerText(),/咖啡/);
      assert.equal(await page.locator('input[aria-label="物品 ID"]').count(),0);
      await page.locator('#eventGiftStage').selectOption('2');
      assert.equal(await page.locator('.event-gift-item').count(),0);
      assert.match(await page.locator('#eventStageGiftPlans').innerText(),/没有奖励配置/);
      await page.locator('#eventGiftStage').selectOption('1');
      assert.equal(await page.locator('.event-gift-item').count(),2);
      await page.locator('.event-gift-item input[aria-label="物品数量"]').nth(1).fill('3');
      await page.getByRole('button',{name:'＋ 添加奖励物品'}).click();
      await page.locator('#eventGiftResults .result').first().waitFor();
      assert.ok(await page.locator('#eventGiftGroups button[data-group]').count()>0);
      assert.match(await page.locator('#eventGiftGroups').innerText(),/食物与饮料/);
      assert.doesNotMatch(await page.locator('#eventGiftResults').innerText(),/竿坯组件/);
      await page.locator('#eventGiftResults button').filter({hasText:'可可'}).click();
      await page.getByRole('button',{name:'保存全部奖励'}).click();
      await page.waitForFunction(()=>document.querySelector('.toast')?.textContent.includes('请重启游戏客户端'));
      await page.waitForFunction(()=>document.querySelector('.event-gift-item .picked')?.innerText.includes('咖啡'));
      assert.match(await page.locator('.event-gift-item .picked').first().innerText(),/咖啡/);
      const saved=writes.at(-1);
      assert.equal(saved.key,'event_stage_gifts');
      assert.deepEqual(JSON.parse(saved.value),{13:{1:[{item:'fcb_coffee_can2',qty:1},{item:'ab_tea_can2',qty:3},{item:'fcb_cocoa_can2',qty:1}]}});
      // 回归：保存当前阶段 2 后，页面仍应停留在阶段 2，不能回到默认阶段 1。
      await page.locator('#eventGiftStage').selectOption('2');
      await page.getByRole('button',{name:'＋ 添加奖励物品'}).click();
      await page.locator('#eventGiftResults .result').first().waitFor();
      await page.locator('#eventGiftResults button').filter({hasText:'咖啡'}).click();
      await page.getByRole('button',{name:'保存全部奖励'}).click();
      await page.waitForFunction(()=>document.querySelector('.toast')?.textContent.includes('请重启游戏客户端'));
      assert.equal(await page.locator('#eventGiftStage').inputValue(),'2');
      const savedStage2=writes.at(-1);
      assert.deepEqual(JSON.parse(savedStage2.value),{13:{1:[{item:'fcb_coffee_can2',qty:1},{item:'ab_tea_can2',qty:3},{item:'fcb_cocoa_can2',qty:1}],2:[{item:'fcb_coffee_can2',qty:1}]}});
      const metrics=await page.locator('.event-gift-card').evaluate(card=>({viewport:innerWidth,scroll:document.documentElement.scrollWidth,controls:[...card.querySelectorAll('input,select,button')].map(e=>{const r=e.getBoundingClientRect();return{x:r.x,right:r.right,height:r.height};})}));
      assert.ok(metrics.scroll<=metrics.viewport+2,JSON.stringify(metrics));
      for(const control of metrics.controls)assert.ok(control.x>=-1&&control.right<=metrics.viewport+2&&control.height>=15,JSON.stringify({width,control,metrics}));
      assert.deepEqual(errors,[]);
      await context.close();
    }
  }finally{await browser.close()}
});
