import { editorImageHtml } from "./editor-media.js";
import { sanitizePastedEditorHtml } from "./editor-paste.js";

export type ImportedImageClaim = {
  id: string;
  claimToken: string;
  url: string;
};

export type ImportedImageFile = {
  id: string;
  claimToken?: string;
  url: string;
  name: string;
  mimeType: string;
  sizeBytes: number;
  storageProvider: "local" | "oss" | "r2";
  storageKey: string;
  storedFilename: string;
};

export type ImageImportFailure = {
  code: string;
  message: string;
  resolution: string;
};

export type PendingImageImport = {
  id: string;
  source: string;
  name: string;
  failure: ImageImportFailure;
};

type ImageSlot = {
  id: string;
  token: string;
  source: string;
  name: string;
};

export type ImageLocalizer = {
  uploadEmbeddedImage: (source: string, name: string) => Promise<ImportedImageFile>;
  importRemoteImages: (items: ReadonlyArray<{ clientId: string; url: string; name: string }>) => Promise<ReadonlyMap<string, ImportedImageFile | ImageImportFailure>>;
};

export type LocalizedPaste = {
  html: string;
  importedImages: ImportedImageClaim[];
  failedImages: PendingImageImport[];
};

const unsupportedSource: ImageImportFailure = {
  code: "INVALID_SOURCE_URL",
  message: "没有找到可转存的图片地址",
  resolution: "请重新粘贴图片，或上传本地图片"
};

/**
 * Localizes every image discovered in clipboard HTML. Original image nodes are
 * replaced before sanitization, so data/blob and lazy-load sources never slip
 * through as an external <img> after the sanitizer removes their attributes.
 */
export async function localizePastedImages(html: string, baseUrl: string, localizer: ImageLocalizer): Promise<LocalizedPaste | null> {
  const prepared = preparePastedImageSlots(html, baseUrl);
  if (!prepared) return null;

  const replacements = new Map<string, string>();
  const importedImages: ImportedImageClaim[] = [];
  const failedImages: PendingImageImport[] = [];
  const remoteSlots = prepared.slots.filter((slot) => slot.source && !isEmbeddedImageSource(slot.source));
  let remoteResults = new Map<string, ImportedImageFile | ImageImportFailure>();

  if (remoteSlots.length > 0) {
    try {
      remoteResults = new Map(await localizer.importRemoteImages(remoteSlots.map((slot) => ({ clientId: slot.id, url: slot.source, name: slot.name }))));
    } catch {
      for (const slot of remoteSlots) {
        remoteResults.set(slot.id, {
          code: "IMPORT_UNAVAILABLE",
          message: "图片转存服务暂时不可用",
          resolution: "请稍后重试，或上传本地图片"
        });
      }
    }
  }

  for (const slot of prepared.slots) {
    let imported: ImportedImageFile | undefined;
    let failure: ImageImportFailure | undefined;
    if (!slot.source) {
      failure = unsupportedSource;
    } else if (isEmbeddedImageSource(slot.source)) {
      try {
        imported = await localizer.uploadEmbeddedImage(slot.source, slot.name);
      } catch {
        failure = {
          code: "LOCAL_UPLOAD_FAILED",
          message: "无法上传剪贴板中的图片",
          resolution: "请重试，或将图片保存到本地后上传"
        };
      }
    } else {
      const remoteResult = remoteResults.get(slot.id);
      if (remoteResult && "url" in remoteResult) {
        imported = remoteResult;
      } else {
        failure = remoteResult ?? unsupportedSource;
      }
    }

    if (imported) {
      replacements.set(slot.token, editorImageHtml(imported.url, imported.name));
      if (imported.claimToken) {
        importedImages.push({ id: imported.id, claimToken: imported.claimToken, url: imported.url });
      }
      continue;
    }

    const exactFailure = failure ?? unsupportedSource;
    replacements.set(slot.token, imageImportFailureHtml(slot.id, exactFailure));
    failedImages.push({ id: slot.id, source: slot.source, name: slot.name, failure: exactFailure });
  }

  let localizedHTML = prepared.html;
  for (const [token, replacement] of replacements) {
    localizedHTML = localizedHTML.replaceAll(`<span>${token}</span>`, replacement).replaceAll(token, replacement);
  }
  return { html: localizedHTML, importedImages, failedImages };
}

export function imageImportFailureHtml(id: string, failure: ImageImportFailure): string {
  return `<figure class="editor-media-import-error" data-fp-type="image-import-error" data-import-id="${escapeAttribute(id)}" contenteditable="false"><strong>图片未转存：${escapeHtml(failure.message)}</strong><span>${escapeHtml(failure.resolution)}</span><div><button type="button" data-image-import-action="retry">重试</button><button type="button" data-image-import-action="upload-local">上传本地图片</button><button type="button" data-image-import-action="remove">删除</button></div></figure>`;
}

function preparePastedImageSlots(html: string, baseUrl: string): { html: string; slots: ImageSlot[] } | null {
  if (!html || !/<img[\s>]/i.test(html)) return null;
  const template = document.createElement("template");
  template.innerHTML = html;
  template.content.querySelectorAll("script,style").forEach((node) => node.remove());
  const images = [...template.content.querySelectorAll<HTMLImageElement>("img")];
  if (images.length === 0) return null;

  const slots: ImageSlot[] = [];
  for (const [index, image] of images.entries()) {
    const id = `paste-image-${createClientID()}-${index}`;
    const token = `FREEDOMPOST_IMAGE_SLOT_${id}`;
    const source = imageSourceFromClipboard(image, baseUrl);
    const name = image.getAttribute("alt")?.trim() || image.getAttribute("title")?.trim() || filenameFromSource(source) || "图片";
    slots.push({ id, token, source, name });

    const parent = image.parentElement;
    if (parent?.tagName === "A" && parent.childElementCount === 1 && !parent.textContent?.trim()) {
      parent.replaceWith(document.createTextNode(token));
    } else {
      const marker = document.createElement("span");
      marker.textContent = token;
      image.replaceWith(marker);
    }
  }
  return { html: sanitizePastedEditorHtml(template.innerHTML, baseUrl), slots };
}

function imageSourceFromClipboard(image: HTMLImageElement, baseUrl: string): string {
  const src = image.getAttribute("src");
  const lazyCandidates = [
    image.getAttribute("data-src"),
    image.getAttribute("data-original"),
    image.getAttribute("data-lazy-src"),
    bestSourceSetCandidate(image.getAttribute("srcset")),
    bestSourceSetCandidate(image.getAttribute("data-srcset")),
    // <picture><source srcset="..."> – modern responsive image pattern used by WeChat articles,
    // Zhihu, and other sites that wrap <img> in a <picture> element.
    ...[...image.closest("picture")?.querySelectorAll<HTMLSourceElement>("source") ?? []].map(
      (source) => bestSourceSetCandidate(source.getAttribute("srcset"))
    )
  ];
  // Many editors put a transparent data URI in src and the real image in a
  // lazy-load attribute. Prefer the latter only in that specific case; a real
  // clipboard data URI still uploads directly when no lazy source exists.
  const candidates = src?.trim().startsWith("data:") && lazyCandidates.some(Boolean) ? [...lazyCandidates, src] : [src, ...lazyCandidates];
  for (const candidate of candidates) {
    const source = candidate?.trim();
    if (!source) continue;
    if (source.startsWith("data:") || source.startsWith("blob:")) return source;
    try {
      return new URL(source, baseUrl).toString();
    } catch {
      return source;
    }
  }
  return "";
}

function bestSourceSetCandidate(value: string | null): string | null {
  if (!value) return null;
  const candidates = value.split(",").map((candidate) => candidate.trim().split(/\s+/, 1)[0]).filter(Boolean);
  return candidates.at(-1) ?? null;
}

function filenameFromSource(source: string): string | null {
  if (!source) return null;
  try {
    const path = new URL(source, location.origin).pathname;
    return decodeURIComponent(path.split("/").at(-1) ?? "") || null;
  } catch {
    return null;
  }
}

function isEmbeddedImageSource(source: string): boolean {
  return source.startsWith("data:") || source.startsWith("blob:");
}

export function createClientID(): string {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function escapeAttribute(value: string): string {
  return value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;").replaceAll("'", "&#039;");
}

function escapeHtml(value: string): string {
  return escapeAttribute(value);
}
