const p = require('puppeteer-core');
(async () => {
  const b = await p.launch({ executablePath: '/usr/bin/google-chrome', args: ['--no-sandbox', '--user-data-dir=/tmp/chprof2'] });
  const pg = await b.newPage(); await pg.setViewport({ width: 1440, height: 900 });
  const errs = []; pg.on('pageerror', (e) => errs.push(e.message)); pg.on('dialog', (d) => d.accept());
  await pg.goto('http://127.0.0.1:8711/chat'); await pg.click('#rail-quotes'); await pg.waitForSelector('.q-tr[data-id]');
  console.log('list rows', await pg.$$eval('.q-tr[data-id]', (r) => r.length));
  await pg.click('#q-new'); await pg.waitForSelector('.q-seg-b');
  console.log('new default kind:', await pg.$eval('.q-seg-b.on', (e) => e.textContent));
  const fields = await pg.$$('.q-cust .q-in'); await fields[0].type('Cliente Libre Demo');
  await pg.type('.q-desc', 'Servicio de instalación'); const nums = await pg.$$('.q-item .q-num-in');
  await nums[0].click({ clickCount: 3 }); await nums[0].type('3'); await nums[1].type('100');
  await pg.waitForFunction(() => document.querySelector('.q-cycle .q-total + dd, .q-cycle dd.q-total'), { timeout: 5000 });
  console.log('totals:', await pg.$eval('.q-cycles', (e) => e.innerText.replace(/\n/g, ' | ')));
  // switch to registered and create customer
  await pg.click('.q-seg-b:not(.on)'); await pg.waitForSelector('.q-cust-opt');
  console.log('customer options', await pg.$$eval('.q-cust-opt', (r) => r.length));
  const nc = (await pg.$$('.q-cust-search .q-btn'))[0]; await nc.click(); await pg.waitForSelector('.q-newcust');
  const nf = await pg.$$('.q-newcust .q-in'); await nf[0].type('Nuevo Cliente SAC'); await nf[2].type('20600000005');
  await pg.click('.q-newcust .q-btn.primary'); await pg.waitForSelector('.q-cust-card');
  console.log('selected:', await pg.$eval('.q-cust-card strong', (e) => e.textContent));
  await pg.click('#q-save'); await pg.waitForFunction(() => document.querySelector('.q-ptitle h2').textContent === 'Borrador' && !document.querySelector('#q-dirty:not([hidden])'));
  console.log('saved; hash', await pg.evaluate(() => location.hash.slice(0, 12)));
  await pg.click('#q-req'); await pg.waitForSelector('#q-approve'); console.log('status', await pg.$eval('.q-status', (e) => e.textContent));
  await pg.click('#q-approve'); await pg.waitForSelector('#q-revise'); console.log('approved', await pg.$eval('.q-ptitle h2', (e) => e.textContent));
  await pg.screenshot({ path: '/workspace/devbrain/screens/qb-1440-flow-approved.png' });
  await pg.click('#q-revise'); await pg.waitForSelector('#q-save'); console.log('revised', await pg.$eval('.q-ptitle', (e) => e.innerText.replace(/\n/g, ' ')));
  // validation
  await pg.click('#q-new').catch(() => {}); 
  await pg.click('#rail-chats'); console.log('back to chat: composer visible', await pg.$eval('#composer', (e) => !e.hidden), 'panel hidden', await pg.$eval('#q-panel', (e) => e.hidden));
  console.log('page errors', JSON.stringify(errs)); await b.close();
})().catch((e) => { console.error('FAIL', e.message); process.exit(1); });
