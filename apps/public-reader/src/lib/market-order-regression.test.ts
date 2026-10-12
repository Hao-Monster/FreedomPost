import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const portalScript = readFileSync(new URL("../scripts/portal.ts", import.meta.url), "utf8");
const marketPage = readFileSync(new URL("../pages/market.astro", import.meta.url), "utf8");

describe("market order automatic handoff regression", () => {
  it("does not use clipboard or an identity change as an automatic order delivery step", () => {
    expect(portalScript).not.toContain("copyTextToClipboard(orderSupportMessage(order))");
    expect(portalScript).not.toContain("widget.setUser?.(getChatwootVisitorId()");
    expect(portalScript).not.toContain("await waitForChatwootConversation(5000)");
  });

  it("keeps the order status and recovery controls outside the dialog that closes for support", () => {
    const statusPosition = marketPage.indexOf('id="marketOrderHandoffs"');
    const dialogPosition = marketPage.indexOf('<dialog id="orderDialog"');
    expect(statusPosition).toBeGreaterThan(-1);
    expect(statusPosition).toBeLessThan(dialogPosition);
    expect(portalScript).not.toContain("打开客服并复制订单信息");
    expect(portalScript).not.toContain("请在客服窗口粘贴发送");
  });
});
