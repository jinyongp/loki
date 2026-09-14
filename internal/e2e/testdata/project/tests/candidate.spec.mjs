import { expect, test } from "@playwright/test";

test("uses the candidate browser without a Playwright download", async ({ page }) => {
  await page.setContent(
    '<h1>Loki candidate</h1><a id="download" download="fixture.txt" href="data:text/plain;charset=utf-8,loki-e2e">download</a>',
  );
  await expect(page.locator("h1")).toHaveText("Loki candidate");
  const [download] = await Promise.all([
    page.waitForEvent("download"),
    page.locator("#download").click(),
  ]);
  expect(download.suggestedFilename()).toBe("fixture.txt");
});
