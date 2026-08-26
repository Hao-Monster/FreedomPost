import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const portalScript = readFileSync(new URL("../scripts/portal.ts", import.meta.url), "utf8");
const portalStyles = readFileSync(new URL("../styles/portal.css", import.meta.url), "utf8");
const marketPage = readFileSync(new URL("../pages/market.astro", import.meta.url), "utf8");

describe("market product actions", () => {
  it("offers details, sharing and ordering directly from every product card", () => {
    expect(portalScript).toContain('data-market-action="details"');
    expect(portalScript).toContain('data-market-action="share"');
    expect(portalScript).toContain('data-market-action="order"');
  });

  it("keeps the product actions visible while long details scroll", () => {
    expect(marketPage).toContain("product-details-dialog");
    expect(portalScript).toContain("product-dialog-scroll");
    expect(portalStyles).toMatch(/\.product-details-dialog[\s\S]*overflow:\s*hidden/);
    expect(portalStyles).toMatch(/\.product-dialog-scroll[\s\S]*overflow-y:\s*auto/);
  });

  it("shows the configured commission on product cards and affiliate pricing rows", () => {
    expect(portalScript).toContain('<span class="market-product-share-label">分享赚钱</span><span class="market-product-commission">每单赚');
    expect(portalScript).toContain("专属链接下单每单赚");
    expect(portalStyles).toMatch(/\.market-product-share \.button\s*\{[^}]*flex-direction:\s*column;[^}]*gap:\s*1px;/s);
  });
});
