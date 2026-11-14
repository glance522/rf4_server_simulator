// 地图区域编辑器 UI：纸地图渲染 + 拖拽吸附 40m 网格 + 本图名册选鱼 + 保存回传。
// 运行前提：npm i --no-save playwright；浏览器可用 RF4_TEST_BROWSER 指定（默认系统 Edge）。
// 期望值按 docs/fish-query-coordinates.md 公式手算（图像像素=世界×scale+offset；网格=floor(世界/40)），
// 不用被测代码复算。
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {chromium}=require('playwright');

// 旧奥斯特罗格湖标定（与 fishmaps.Scenes 同源）：origin 0/0，scale 4.824，offset 56/37。
// 拖拽 world(85,85)→(195,155)：起点格 floor(85/40)=2，终点格 floor(195/40)=4 / floor(155/40)=3。
const CAL={origin_x:0,origin_z:0,size:400,paper:{offset_x:56,offset_z:37,scale:4.824,width:2048,height:2048}};

test('zone map editor draws on paper map with 40m grid snap',async()=>{
 const html=fs.readFileSync(path.join(__dirname,'web/index.html'));
 const webDir=path.join(__dirname,'web');
 const posts=[];
 const server=http.createServer((req,res)=>{
  const url=new URL(req.url,'http://fixture'),key=url.pathname.replace(/^\/admin/,'');
  const json=(value,status=200)=>{res.writeHead(status,{'Content-Type':'application/json'});res.end(JSON.stringify(value));};
  if(key==='/api/me')return json({username:'admin'});
  if(key==='/api/fish/areas')return json([
   {loc:'旧奥斯特罗格湖',fish_count:16,level:'level_004_torfyanoe_ozero',map_kind:'grid',texture:'maps/map_level_004_torfyanoe.webp',calibration:CAL},
   {loc:'挪威海',fish_count:57,level:'level_012_north_sea',map_kind:'sea',texture:'maps/map_level_012_north_sea.webp',calibration:{origin_x:0,origin_z:0,size:0,paper:{offset_x:78,offset_z:78,scale:0.38,width:2048,height:2048}}},
   {loc:'无名塘',fish_count:3,level:'',map_kind:'none',texture:'',calibration:null}]);
  if(key==='/api/fish/bite_mode')return json({mode:'realistic'});
  if(key==='/api/tunables')return json({});
  if(key==='/api/bait/prefs')return json({rows:[]});
  if(key==='/api/fish/config'){
   const loc=url.searchParams.get('loc');
   if(loc==='挪威海')return json({area:'挪威海',setting:{bite_probability:1,bite_delay_fixed:0,bite_delay_scale:1,bite_delay_max:20,bite_delay_lure_max:6},fish:[{fish_key:'cod',name_zh:'鳕鱼',base_weight:2,weight:2,enabled:true}]});
   if(loc==='无名塘')return json({area:'无名塘',setting:{bite_probability:1,bite_delay_fixed:0,bite_delay_scale:1,bite_delay_max:20,bite_delay_lure_max:6},fish:[]});
   return json({area:'旧奥斯特罗格湖',setting:{bite_probability:1,bite_delay_fixed:0,bite_delay_scale:1,bite_delay_max:20,bite_delay_lure_max:6},fish:[
    {fish_key:'crucian',name_zh:'银鲫',base_weight:3.2,weight:3.2,enabled:true},
    {fish_key:'pike',name_zh:'狗鱼',base_weight:5,weight:5,enabled:true},
    {fish_key:'bream',name_zh:'欧鳊',base_weight:2.1,weight:2.1,enabled:true}]});
  }
  if(key==='/api/fish/sea-zones')return json({zones:[
   {zone:120,x0:3450,z0:850,x1:3690,z1:630},
   {zone:121,x0:3690,z0:850,x1:3930,z1:630}]});
  if(key==='/api/fish/zones'&&req.method==='GET')return json({zones:[
   {id:7,loc:'旧奥斯特罗格湖',name:'湾口鲤区',kind:'grid_rect',sea_zone:0,gx0:0,gz0:0,gx1:2,gz1:1,mode:'exclusive',boost:3,keep_others:0.35,enabled:true,fish:[{fish_key:'crucian',weight:3,enabled:true}]}]});
  if(key.startsWith('/api/')){
   let body='';req.on('data',c=>body+=c);req.on('end',()=>{posts.push({key,body});json({ok:true,id:9});});
   return;
  }
  const file=path.join(webDir,key.replace(/^\/+/,''));
  if(fs.existsSync(file)&&fs.statSync(file).isFile()){
   res.writeHead(200,{'Content-Type':key.endsWith('.webp')?'image/webp':'text/html'});
   return res.end(fs.readFileSync(file));
  }
  res.writeHead(200,{'Content-Type':'text/html; charset=utf-8'});res.end(html);
 });
 await new Promise(r=>server.listen(0,'127.0.0.1',r));
 let browser;
 try{
  browser=await chromium.launch({headless:true,executablePath:process.env.RF4_TEST_BROWSER||path.join(process.env['ProgramFiles(x86)'],'Microsoft/Edge/Application/msedge.exe')});
  const context=await browser.newContext({viewport:{width:1440,height:900}});
  await context.addInitScript(()=>{localStorage.setItem('rf4_local_admin_token','fixture');localStorage.setItem('rf4_admin_token','fixture');});
  const page=await context.newPage(),errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await page.goto(`http://127.0.0.1:${server.address().port}/admin/#fish`,{waitUntil:'networkidle'});
  await page.locator('#fishTabZones').click();
  // 纸地图加载后才能交互
  await page.waitForFunction(()=>{const i=document.getElementById('zoneMapImg');return i&&i.complete&&i.naturalWidth>0;});
  assert.ok((await page.locator('#zoneMapImg').getAttribute('src')).includes('map_level_004_torfyanoe.webp'),'贴图 URL 应含特例名');

  // 拖拽 world(85,85)→(195,155)：期望网格 2:2 ~ 4:3
  // 注意 Z 轴：客户端纸地图公式的 Y 从图片底部往上量，位图行号从顶部往下数 → 行 = H−(w×scale+off)
  const rect=await page.locator('#zoneMapStage').boundingBox();
  const w2pxX=(w,off)=>(w*CAL.paper.scale+off)/2048;
  const w2pxZ=(w,off)=>(2048-(w*CAL.paper.scale+off))/2048;
  const sx=rect.x+w2pxX(85,CAL.paper.offset_x)*rect.width, sy=rect.y+w2pxZ(85,CAL.paper.offset_z)*rect.height;
  const ex=rect.x+w2pxX(195,CAL.paper.offset_x)*rect.width, ey=rect.y+w2pxZ(155,CAL.paper.offset_z)*rect.height;
  await page.mouse.move(sx,sy);await page.mouse.down();
  await page.mouse.move((sx+ex)/2,(sy+ey)/2);
  await page.mouse.move(ex,ey);await page.mouse.up();
  await page.locator('#zonePanel:not([hidden])').waitFor();
  const val=id=>page.locator('#'+id).inputValue();
  assert.equal(await val('zoneGX0'),'2');assert.equal(await val('zoneGZ0'),'2');
  assert.equal(await val('zoneGX1'),'4');assert.equal(await val('zoneGZ1'),'3');

  // 本图名册选鱼：列表来自 /api/fish/config 的鱼种，带基础权重
  await page.locator('.zone-fish-row[data-key="pike"]').click();
  await page.locator('.zone-fish-row[data-key="bream"]').click();
  await page.fill('#zoneName','湾嘴狗鱼区');
  await page.locator('#zonePanelFooter button:has-text("保存区域")').click();
  await page.waitForFunction(()=>document.querySelectorAll('.toast div').length>0);
  const saved=posts.find(p=>p.key==='/api/fish/zones');
  assert.ok(saved,'应 POST /api/fish/zones');
  const body=JSON.parse(saved.body);
  assert.equal(body.gx0,2);assert.equal(body.gz0,2);assert.equal(body.gx1,4);assert.equal(body.gz1,3);
  assert.equal(body.mode,'exclusive');assert.equal(body.name,'湾嘴狗鱼区');
  assert.deepEqual(body.fish.map(f=>f.fish_key).sort(),['bream','pike']);
  assert.equal(body.fish.find(f=>f.fish_key==='pike').weight,5,'名册加入时默认带基础权重');

  // 点击既有区域 → 面板载入该区（保存后布局有变化，重新量舞台坐标）
  const rect2=await page.locator('#zoneMapStage').boundingBox();
  await page.mouse.click(rect2.x+w2pxX(40,CAL.paper.offset_x)*rect2.width, rect2.y+w2pxZ(20,CAL.paper.offset_z)*rect2.width);
  await page.waitForTimeout(150);
  assert.equal(await val('zoneName'),'湾口鲤区');
  assert.equal(await val('zoneGX1'),'2');

  // 海图：纯浏览（2026-11-14 起不做区域划分）——底图加载、建区入口全部隐藏
  await page.locator('.player-row[data-loc="挪威海"]').click();
  await page.waitForFunction(()=>{const i=document.getElementById('zoneMapImg');return i&&i.complete&&i.naturalWidth>0;},{timeout:8000});
  await page.waitForTimeout(500);
  assert.equal(await page.locator('#zoneNewRectBtn').isHidden(),true,'海图应隐藏建区按钮');
  assert.equal(await page.locator('#zoneSeaNowBtn').isHidden(),true,'海图应隐藏📡按钮');
  assert.equal(await page.locator('#zoneSeaLatestBtn').isHidden(),true,'海图应隐藏📍按钮');
  const note=await page.locator('#zoneMapNote').textContent();
  assert.ok(note.includes('不做区域划分'),'海图提示应说明不做区域划分');
  // 无图地图：手动输入入口可见
  await page.locator('.player-row[data-loc="无名塘"]').click();
  await page.waitForTimeout(400);
  assert.equal(await page.locator('#zoneManualBtn').isHidden(),false,'无图地图应显示手动输入');
  assert.equal(await page.locator('#zoneMapImg').getAttribute('src'),null,'无图地图不应有贴图 src');

  assert.deepEqual(errors,[],'页面不应有脚本错误');
 }finally{
  if(browser)await browser.close();
  server.close();
 }
});
