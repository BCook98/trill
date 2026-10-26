// Browser regression suite: npm --prefix website test; see website/README.md.
const { chromium, webkit } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const base = process.env.TRILL_DEMO_URL || 'http://127.0.0.1:8780/';
const output = process.env.TRILL_DEMO_ARTIFACTS || '.research/website-demo';
const demoURL = new URL('#demo', base).href;
fs.mkdirSync(output, { recursive: true });
async function decodeImages(page) {
 await page.evaluate(async()=>{
   const images=Array.from(document.images);
   // Hidden lazy fallback images may never enter the viewport. Request them
   // explicitly for screenshot verification, even when the video is visible.
   images.forEach(img=>img.loading='eager');
   await Promise.race([
     Promise.all(images.map(img=>img.decode())),
     new Promise((_,reject)=>setTimeout(()=>reject(new Error('Image decode timed out')),15000))
   ]);
 });
}
(async()=>{
for (const name of ['chromium','webkit']) {
 const browser=await (name==='chromium'?chromium.launch({channel:'chromium',headless:true}):webkit.launch({headless:true}));
 const page=await browser.newPage({viewport:{width:1440,height:800}});
 // The homepage contains the only player; media stays idle until it enters view.
 await page.goto(base);
 assert.equal(await page.locator('h1').count(),1);
 assert.equal(await page.locator('#demo').count(),1);
 assert.equal(await page.locator('.watch-link').getAttribute('href'),'#demo');
 assert.equal(await page.locator('a[href="/demo/"]').count(),0);
 await page.waitForSelector('#demo[data-theme]');
 assert(await page.locator('#cover-video').evaluate(v=>v.paused && v.currentTime===0));
 assert.equal(await page.locator('.hero-actions .primary').getAttribute('href'),'mailto:trill@bcook.io?subject=Trill%20early%20access');
 assert.equal(await page.locator('.hero-actions .primary').textContent(),'Ask about early access');
 if(name==='webkit') await page.locator('.skip').focus();
 else await page.keyboard.press('Tab');
 assert.equal(await page.locator(':focus').textContent(),'Skip to content');
 await page.keyboard.press('Enter');
 assert.equal(new URL(page.url()).hash,'#main');
 for(const width of [320,390,600,760,768,1000,1440]) {
   await page.setViewportSize({width,height:900});
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),`${name} homepage overflow at ${width}`);
 }
 // Capture fresh pages after lazy images decode; focus/anchor scrolling can distort stitched captures.
 for(const [label,width,height] of [['desktop',1440,1000],['mobile',390,844]]) {
   const visual=await browser.newPage({viewport:{width,height},reducedMotion:'reduce'});
   await visual.goto(base);
   await visual.waitForSelector('#demo[data-theme]');
   await visual.locator('.workspace-figure').scrollIntoViewIfNeeded();
   await decodeImages(visual);
   await visual.evaluate(()=>scrollTo({top:0,behavior:'instant'}));
   await visual.screenshot({path:path.join(output,`${name}-home-${label}.png`),fullPage:true});
   await visual.close();
 }
 await page.setViewportSize({width:1440,height:1100});
 const range=await page.request.get(new URL('assets/demos/duo-dark-cover.mp4',base).href,{headers:{Range:'bytes=0-99'}});
 assert.equal(range.status(),206,'preview server supports seeking');
 assert.equal((await range.body()).length,100);
 const noJS=await browser.newContext({javaScriptEnabled:false,viewport:{width:390,height:844}});
 const fallback=await noJS.newPage();await fallback.goto(base);
 assert(await fallback.locator('.hero-actions .primary').isVisible());
 await fallback.goto(demoURL);
 assert(await fallback.locator('noscript a').first().isVisible());
 assert(await fallback.locator('.demo-fallback img').isVisible());
 await fallback.goto(new URL('demo/',base).href);
 await fallback.waitForURL(url=>url.pathname==='/' && url.hash==='#demo');
 await noJS.close();
 // Keep the header/footer and documents usable at narrow widths and with every FAQ open.
 for(const route of ['support/','privacy/','404.html']) {
   await page.goto(new URL(route,base).href);
   await page.evaluate(()=>document.querySelectorAll('details').forEach(el=>el.open=true));
   for(const width of [320,390,768,1440]) {
     await page.setViewportSize({width,height:900});
     assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),`${name} ${route} overflow at ${width}`);
   }
 }
 await page.setViewportSize({width:1440,height:1100});

 const errors=[]; page.on('pageerror',error=>errors.push(error.message));
 // One automatically playing Duo sequence; appearance comes from the browser.
 await page.emulateMedia({colorScheme:'light'});
 await page.goto(demoURL);
 await page.waitForFunction(()=>document.querySelector('#cover-video').currentTime>.2);
 assert.equal(await page.locator('#demo').getAttribute('data-theme'),'light');
 assert.equal(await page.locator('input, video[controls], [data-device-choice], [data-theme-choice]').count(),0);
 assert.equal(await page.locator('video').count(),2);
 await page.screenshot({path:path.join(output,`${name}-demo-cover-light.png`),fullPage:true});
 await page.locator('#demo-toggle').click();
 assert(await page.locator('#cover-video').evaluate(v=>v.paused));
 await page.locator('#demo-toggle').click();
 await page.waitForFunction(()=>!document.querySelector('#cover-video').paused);
 // Theme changes retain workflow progress and keep playing, including while unfolded.
 await page.locator('#cover-video').evaluate(v=>v.currentTime=8);
 await page.emulateMedia({colorScheme:'dark'});
 await page.waitForFunction(()=>document.querySelector('#demo').dataset.theme==='dark' && document.querySelector('#cover-video').currentTime>7 && !document.querySelector('#cover-video').paused);
 assert.equal(await page.locator('#demo').getAttribute('data-theme'),'dark');
 await page.screenshot({path:path.join(output,`${name}-demo-cover-dark.png`),fullPage:true});
 await page.locator('#cover-video').evaluate(v=>v.currentTime=v.duration-.2);
 await page.waitForFunction(()=>document.querySelector('#demo').dataset.phase==='open' && document.querySelector('#open-video').currentTime>.2);
 await page.screenshot({path:path.join(output,`${name}-demo-open-dark.png`),fullPage:true});
 await page.emulateMedia({colorScheme:'light'});
 await page.waitForFunction(()=>document.querySelector('#demo').dataset.theme==='light' && document.querySelector('#open-video').currentTime>.2 && !document.querySelector('#open-video').paused);
 await page.locator('#open-video').evaluate(v=>v.currentTime=12);
 await page.screenshot({path:path.join(output,`${name}-demo-open-light.png`),fullPage:true});
 // Offscreen pause/resume; an intentional pause stays paused.
 await page.evaluate(()=>{ document.body.style.paddingBottom='1500px'; scrollTo({top:document.body.scrollHeight,behavior:'instant'}); });
 await page.waitForFunction(()=>document.querySelector('#open-video').paused);
 await page.locator('#demo-stage').scrollIntoViewIfNeeded();
 await page.waitForFunction(()=>!document.querySelector('#open-video').paused);
 await page.locator('#demo-toggle').click();
 await page.evaluate(()=>scrollTo({top:document.body.scrollHeight,behavior:'instant'}));
 await page.locator('#demo-stage').scrollIntoViewIfNeeded();
 assert(await page.locator('#open-video').evaluate(v=>v.paused));
 await page.locator('#demo-toggle').click();
 await page.locator('#open-video').evaluate(v=>v.currentTime=v.duration-.2);
 await page.waitForFunction(()=>document.querySelector('#demo-toggle').getAttribute('aria-label')==='Replay demo');
 await page.locator('#demo-toggle').click();
 await page.waitForFunction(()=>document.querySelector('#cover-video').currentTime>.1 && !document.querySelector('#cover-video').paused);
 await page.locator('#demo-toggle').click();
 await page.evaluate(()=>document.body.style.paddingBottom='');
 for (const width of [320,390,600,760,768,1000,1440]) {
   await page.setViewportSize({width,height:900});
   assert(!await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),`${name} demo overflow at ${width}`);
 }
 await page.setViewportSize({width:390,height:844});
 await page.screenshot({path:path.join(output,`${name}-demo-mobile.png`),fullPage:true});
 await page.emulateMedia({reducedMotion:'reduce'});
 await page.reload();
 await page.waitForSelector('#demo[data-theme]');
 assert(await page.locator('#open-video').evaluate(v=>v.paused));
 assert.equal(await page.locator('#demo').getAttribute('data-phase'),'open');
 assert(await page.locator('#demo-toggle-label').isVisible());
 await page.locator('#demo-toggle').click();
 await page.waitForFunction(()=>document.querySelector('#open-video').currentTime>.1);
 // No Javascript fallback and support/privacy stay usable in either scheme.
 for (const scheme of ['light','dark']) {
   await page.emulateMedia({colorScheme:scheme});
   await page.goto(base);
   await page.waitForSelector('#demo[data-theme]');
   assert.equal(await page.locator('.hero-product, .phone-image').count(),0);
   const still=await page.locator('.demo-fallback img').evaluate(img=>img.currentSrc);
   assert(still.includes(`duo-${scheme}-preview.webp`));
   await page.locator('.workspace-figure').scrollIntoViewIfNeeded();
   await decodeImages(page);
   await page.evaluate(()=>scrollTo({top:0,behavior:'instant'}));
   await page.screenshot({path:path.join(output,`${name}-home-mobile-${scheme}.png`),fullPage:true});
 }
 // Browsers that disallow autoplay expose one accessible Play action.
 const restricted=await browser.newContext();
 await restricted.addInitScript(()=>{
   const original=HTMLMediaElement.prototype.play;
   let first=true;
   HTMLMediaElement.prototype.play=function(){
     if(first){first=false;return Promise.reject(new DOMException('Blocked','NotAllowedError'));}
     return original.call(this);
   };
 });
 const blocked=await restricted.newPage(); await blocked.goto(demoURL);
 await blocked.waitForFunction(()=>document.querySelector('#demo-status').textContent==='Tap to watch the demo.');
 await blocked.locator('#demo-toggle').click();
 await blocked.waitForFunction(()=>document.querySelector('#cover-video').currentTime>.1);
 await restricted.close();
 const failed=await browser.newPage(); let failOnce=true;
 await failed.route('**/duo-*-cover.mp4',route=>{
   if(failOnce){failOnce=false;return route.abort();}
   return route.continue();
 });
 await failed.goto(demoURL);
 await failed.waitForFunction(()=>document.querySelector('#demo-status').textContent.includes('retry'));
 await failed.locator('#demo-toggle').click();
 await failed.waitForFunction(()=>document.querySelector('#cover-video').currentTime>.1);
 await failed.close();
 assert.deepEqual(errors,[]);
 await browser.close(); console.log(`${name}: autoplay, fold, theme changes, pause/resume, replay, reduced motion, responsive pages passed`);
}
})().catch(error=>{console.error(error);process.exit(1);});
