const { chromium } = require("@playwright/test");

const BASE = "http://127.0.0.1:3001";
const TOKEN = process.env.ADROUTE_TOKEN;

async function waitApp(page) {
  await page.goto(`${BASE}/#token=${TOKEN}`, { waitUntil: "domcontentloaded" });
  await page.waitForSelector("#app-shell:not([hidden])", { timeout: 15000 });
  await page.waitForSelector("#model-list", { state: "attached" });
  await page.waitForTimeout(1200);
}

(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await waitApp(page);
  await page.screenshot({ path: "/tmp/opencode/b-models.png" });

  // open add-model dialog
  await page.click("#add-model");
  await page.waitForSelector("#model-dialog[open]");
  await page.waitForTimeout(400);
  await page.screenshot({ path: "/tmp/opencode/b-dialog.png" });
  await page.keyboard.press("Escape");
  await page.waitForTimeout(300);

  // requests tab
  await page.click('[data-tab="requests"]');
  await page.waitForTimeout(900);
  await page.screenshot({ path: "/tmp/opencode/b-requests.png" });

  // setup tab
  await page.click('[data-tab="setup"]');
  await page.waitForTimeout(900);
  await page.screenshot({ path: "/tmp/opencode/b-setup.png" });

  await browser.close();
  console.log("done");
})().catch((e) => { console.error(e); process.exit(1); });
