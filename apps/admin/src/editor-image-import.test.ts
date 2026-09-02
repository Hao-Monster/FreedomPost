// @vitest-environment happy-dom
import { describe, expect, it } from "vitest";
import { localizePastedImages, type ImageLocalizer } from "./editor-image-import.js";

const imported = {
  id: "attachment-1",
  claimToken: "one-time-claim",
  url: "/api/uploads/image.png",
  name: "image.png",
  mimeType: "image/png" as const,
  sizeBytes: 12,
  storageProvider: "local" as const,
  storageKey: "image.png",
  storedFilename: "image.png"
};

describe("localizePastedImages", () => {
  it("imports external and lazy-load images without leaving the source URL in editor HTML", async () => {
    const requested: string[] = [];
    const localizer: ImageLocalizer = {
      uploadEmbeddedImage: async () => imported,
      importRemoteImages: async (items) => {
        requested.push(...items.map((item) => item.url));
        return new Map(items.map((item, index) => [item.clientId, { ...imported, id: `attachment-${index}`, url: `/api/uploads/${index}.png` }]));
      }
    };
    const result = await localizePastedImages('<p>正文<img src="https://feishu.example/a.png" alt="示例" /><img data-src="https://cdn.example/b.png" /></p>', "https://admin.example/editor", localizer);

    expect(requested).toEqual(["https://feishu.example/a.png", "https://cdn.example/b.png"]);
    expect(result?.html).not.toContain("feishu.example");
    expect(result?.html).not.toContain("cdn.example");
    expect(result?.html).toContain('/api/uploads/0.png');
    expect(result?.importedImages).toHaveLength(2);
  });

  it("uses the highest srcset source when src is absent", async () => {
    const localizer: ImageLocalizer = {
      uploadEmbeddedImage: async () => imported,
      importRemoteImages: async (items) => new Map([[items[0]!.clientId, imported]])
    };
    const result = await localizePastedImages('<img srcset="https://images.example/s.png 1x, https://images.example/l.png 2x" />', "https://admin.example", localizer);

    expect(result?.html).toContain('/api/uploads/image.png');
    expect(result?.html).not.toContain("images.example");
  });

  it("prefers a lazy source over a transparent data-uri placeholder", async () => {
    let requested = "";
    const localizer: ImageLocalizer = {
      uploadEmbeddedImage: async () => imported,
      importRemoteImages: async (items) => {
        requested = items[0]!.url;
        return new Map([[items[0]!.clientId, imported]]);
      }
    };
    await localizePastedImages('<img src="data:image/gif;base64,R0lGODlhAQABAAAAACw=" data-original="https://images.example/original.webp" />', "https://admin.example", localizer);

    expect(requested).toBe("https://images.example/original.webp");
  });

  it("replaces failed imports with an actionable card and never preserves the remote img", async () => {
    const localizer: ImageLocalizer = {
      uploadEmbeddedImage: async () => imported,
      importRemoteImages: async (items) => new Map([[items[0]!.clientId, { code: "SOURCE_HTTP_ERROR", message: "来源拒绝下载", resolution: "上传本地图片" }]])
    };
    const result = await localizePastedImages('<img src="https://private.example/image.png" />', "https://admin.example", localizer);

    expect(result?.html).toContain('data-fp-type="image-import-error"');
    expect(result?.html).not.toContain("private.example");
    expect(result?.failedImages[0]?.failure.code).toBe("SOURCE_HTTP_ERROR");
  });
});
