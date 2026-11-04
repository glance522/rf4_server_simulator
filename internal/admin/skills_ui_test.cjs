const {test}=require('node:test');
const assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const {chromium}=require('playwright');
const html=fs.readFileSync(path.join(__dirname,'web/index.html'),'utf8');

test('permanent skill editor uses 100 percent and fits long skill names',async()=>{
 const browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||undefined});
 const measurements=[];
 try{
  for(const width of [1280,1024,360])for(const dpi of [1,1.25,1.5]){
   const context=await browser.newContext({viewport:{width,height:900},deviceScaleFactor:dpi});
   await context.addInitScript(()=>localStorage.setItem('rf4_admin_token','fixture'));
   const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
   await page.route('**/*',route=>{
    const url=new URL(route.request().url());
    if(url.pathname==='/admin/')return route.fulfill({contentType:'text/html; charset=utf-8',body:html});
    const data=url.pathname==='/admin/api/me'?{username:'admin'}:url.pathname==='/admin/api/players'?[]:{};
    return route.fulfill({contentType:'application/json',body:JSON.stringify(data)});
   });
   await page.goto('http://127.0.0.1:19214/admin/#players');
   await page.locator('#view-players').waitFor({state:'visible'});
   for(const [scenario,name] of [['normal','海钓'],['chinese','最长合理技能名称与说明'.repeat(12)],['continuous','UnbrokenSkillName'.repeat(25)]]){
    await page.evaluate(name=>{
     PLAYER={uid:1,name:'技能测试',level:50,xp:0,item_count:0,keepnet_count:0,mail_count:0,available_points:35,skill_total:113,skill_spent:78,skill_bonus:0,
      skills:[{skill_id:11,name,points:100,node_count:4},{skill_id:1,name:'浮子钓鱼',points:50,node_count:12},{skill_id:2,name:'纺车钓鱼',points:0,node_count:8}]};
     PLAYER_TAB='skills';renderPlayerDetail();
    },name);
    const result=await page.locator('.skills').evaluate(skills=>{
     const rect=e=>{const r=e.getBoundingClientRect();return{x:r.x,y:r.y,right:r.right,bottom:r.bottom,width:r.width,height:r.height};};
     return{viewport:innerWidth,scroll:document.documentElement.scrollWidth,cards:[...skills.querySelectorAll('.skill')].map(card=>{
      const input=card.querySelector('input'),button=card.querySelector('button'),label=card.querySelector('.top>div'),pill=card.querySelector('.pill'),bar=card.querySelector('.bar'),fill=bar.querySelector('i');
      const range=document.createRange();range.selectNodeContents(label);
      return{card:rect(card),input:rect(input),button:rect(button),label:rect(label),pill:rect(pill),max:input.max,font:getComputedStyle(input).fontSize,barWidth:bar.clientWidth,fillWidth:fill.getBoundingClientRect().width,
       textRects:[...range.getClientRects()].map(r=>({x:r.x,y:r.y,right:r.right,bottom:r.bottom}))};
     })};
    });
    assert.ok(result.scroll<=width+1,JSON.stringify({width,dpi,scenario,result}));
    for(const card of result.cards){
     assert.equal(card.max,'100');assert.equal(card.font,'14px');
     assert.ok(card.input.width>=45&&card.button.width>=40&&card.button.height>=28,JSON.stringify({width,dpi,scenario,card}));
     assert.ok(card.button.x-card.input.right>=7);
     assert.ok(card.label.right<=card.pill.x+1);
     for(const item of [card.input,card.button,card.label,card.pill])assert.ok(item.x>=card.card.x&&item.right<=card.card.right+1);
     for(const text of card.textRects)assert.ok(text.x>=card.label.x-1&&text.right<=card.label.right+1&&text.y>=card.label.y-1&&text.bottom<=card.label.bottom+1);
    }
    result.cards.forEach((card,i)=>assert.ok(Math.abs(card.fillWidth-card.barWidth*[1,0.5,0][i])<=1));
    await page.locator('#sk_11').fill('101');assert.equal(await page.locator('#sk_11').evaluate(e=>e.checkValidity()),false);
    await page.locator('#sk_11').fill('100');assert.equal(await page.locator('#sk_11').evaluate(e=>e.checkValidity()),true);
    measurements.push({width,dpi,scenario,cardWidth:result.cards[0].card.width,inputWidth:result.cards[0].input.width,buttonWidth:result.cards[0].button.width,buttonHeight:result.cards[0].button.height,font:result.cards[0].font});
   }
   assert.deepEqual(errors,[]);await context.close();
  }
 }finally{await browser.close();}
 console.log(JSON.stringify({renderedCases:measurements.length,measurements}));
});
