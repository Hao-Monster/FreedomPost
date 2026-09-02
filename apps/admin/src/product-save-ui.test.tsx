// @vitest-environment happy-dom

import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ProductWorkspace } from "./main";

type TestProduct = Parameters<typeof ProductWorkspace>[0]["products"][number];

const product: TestProduct = {
  id: "0eb431aa-1200-4f42-9f04-4d7517b555dc",
  slug: "remote-install",
  title: "未保存的远程安装服务",
  summary: "协助完成远程安装",
  description: "预约后联系管理员安排服务。",
  category: "service",
  priceCents: 3000,
  commissionCents: 500,
  compareAtCents: null,
  currency: "CNY",
  stock: -1,
  soldCount: 19,
  coverUrl: null,
  linkUrl: "",
  status: "published",
  sortOrder: 2,
  createdAt: "2026-09-01T00:00:00Z",
  updatedAt: "2026-09-01T00:00:00Z"
};

let root: Root;
let container: HTMLDivElement;
const showToast = vi.fn();

function Harness({ initialProduct = product, initialProducts }: { initialProduct?: TestProduct; initialProducts?: TestProduct[] }) {
  const [products, setProducts] = useState<TestProduct[]>(initialProducts ?? [initialProduct]);
  return (
    <ProductWorkspace
      products={products}
      setProducts={setProducts}
      onOpenPosts={() => undefined}
      onOpenDistribution={() => undefined}
      onOpenTools={() => undefined}
      onRefresh={async () => undefined}
      onLogout={async () => undefined}
      adminUsername="admin"
      showToast={showToast}
      toast={null}
    />
  );
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("fetch", vi.fn());
  showToast.mockReset();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});

describe("product save feedback UI", () => {
  it("keeps a persistent alert, marks the field, and focuses the first local validation error", async () => {
    await act(async () => root.render(<Harness initialProduct={{ ...product, compareAtCents: 500 }} />));

    const saveButton = [...container.querySelectorAll("button")].find((button) => button.textContent === "保存");
    await act(async () => {
      saveButton?.click();
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    const alert = container.querySelector<HTMLElement>('[role="alert"]');
    const compareAtInput = container.querySelector<HTMLInputElement>('input[aria-describedby="product-compareAtCents-error"]');
    expect(alert?.textContent).toContain("划线价不能低于售价");
    expect(alert?.textContent).toContain("30.00 CNY");
    expect(compareAtInput?.getAttribute("aria-invalid")).toBe("true");
    expect(document.activeElement).toBe(compareAtInput);
    expect(fetch).not.toHaveBeenCalled();
  });

  it("reauthenticates in place without refreshing or replacing the unsaved product", async () => {
    const fetchMock = vi.mocked(fetch);
    fetchMock
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: "UNAUTHENTICATED", message: "未登录" } }), { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ session: { username: "admin" } }), { status: 200 }));
    await act(async () => root.render(<Harness />));

    const saveButton = [...container.querySelectorAll("button")].find((button) => button.textContent === "保存");
    await act(async () => {
      saveButton?.click();
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    const dialog = container.querySelector<HTMLElement>('[role="dialog"]');
    const passwordInput = dialog?.querySelector<HTMLInputElement>('input[type="password"]');
    expect(dialog?.getAttribute("aria-modal")).toBe("true");
    await vi.waitFor(() => expect(document.activeElement).toBe(passwordInput));

    await act(async () => {
      if (passwordInput) {
        passwordInput.value = "correct-password";
        passwordInput.dispatchEvent(new Event("input", { bubbles: true }));
      }
    });
    await act(async () => {
      dialog?.querySelector("form")?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });

    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(container.querySelector<HTMLInputElement>('input[value="未保存的远程安装服务"]')?.value).toBe("未保存的远程安装服务");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls.some(([url, options]) => url === "/api/admin/products" && (!options || options.method === "GET"))).toBe(false);
  });

  it("prevents duplicate saves while one request is still pending", async () => {
    let resolveSave!: (response: Response) => void;
    vi.mocked(fetch).mockReturnValue(new Promise<Response>((resolve) => { resolveSave = resolve; }));
    await act(async () => root.render(<Harness />));

    const saveButton = [...container.querySelectorAll("button")].find((button) => button.textContent === "保存");
    await act(async () => {
      saveButton?.click();
      saveButton?.click();
      await Promise.resolve();
    });

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(saveButton?.disabled).toBe(true);
    expect(saveButton?.textContent).toBe("保存中…");

    await act(async () => {
      resolveSave(new Response(JSON.stringify(product), { status: 200 }));
      await Promise.resolve();
    });
    expect(saveButton?.disabled).toBe(false);
  });

  it("shows create failures even when there is no active product", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ error: { code: "INTERNAL_ERROR", message: "服务暂时不可用" } }), {
      status: 500,
      headers: { "X-Request-ID": "50d20e84-bd74-4d28-b2c2-8f94a1b1675d" }
    }));
    await act(async () => root.render(<Harness initialProducts={[]} />));

    const createButton = container.querySelector<HTMLButtonElement>('button[aria-label="新建商品"]');
    await act(async () => {
      createButton?.click();
      await Promise.resolve();
    });

    const alert = container.querySelector<HTMLElement>('[role="alert"]');
    expect(alert?.textContent).toContain("商品创建失败");
    expect(alert?.textContent).toContain("稍后重试");
    expect(alert?.textContent).toContain("50d20e84-bd74-4d28-b2c2-8f94a1b1675d");
  });
});
