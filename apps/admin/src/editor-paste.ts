const allowedTags = new Set([
  "A",
  "B",
  "BLOCKQUOTE",
  "BR",
  "CODE",
  "DEL",
  "DIV",
  "EM",
  "FIGURE",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "IMG",
  "LI",
  "OL",
  "P",
  "PRE",
  "S",
  "SPAN",
  "STRONG",
  "U",
  "UL"
]);

const removedTags = [
  "base",
  "button",
  "canvas",
  "embed",
  "form",
  "iframe",
  "input",
  "link",
  "math",
  "meta",
  "object",
  "option",
  "script",
  "select",
  "style",
  "svg",
  "textarea"
].join(",");

const allowedInlineClasses = new Set([
  "fp-size-sm",
  "fp-size-md",
  "fp-size-lg",
  "fp-size-xl",
  "fp-color-ink",
  "fp-color-red",
  "fp-color-green",
  "fp-color-blue",
  "fp-color-purple",
  "fp-color-gold"
]);

/**
 * Reduces clipboard HTML to the editor's supported semantic subset. In
 * particular, source-site presentation styles cannot make a heading look like
 * body text in the editor and then change size after Markdown is published.
 */
export function sanitizePastedEditorHtml(html: string, baseUrl: string): string {
  const template = document.createElement("template");
  template.innerHTML = html;
  template.content.querySelectorAll(removedTags).forEach((element) => element.remove());

  const elements = [...template.content.querySelectorAll<HTMLElement>("*")].reverse();
  for (const element of elements) {
    if (!allowedTags.has(element.tagName)) {
      element.replaceWith(...element.childNodes);
      continue;
    }

    const originalAttributes = new Map([...element.attributes].map((attribute) => [attribute.name, attribute.value]));
    for (const attribute of [...element.attributes]) {
      element.removeAttribute(attribute.name);
    }

    if (element.tagName === "A") {
      const href = safeUrl(originalAttributes.get("href"), baseUrl, ["http:", "https:", "mailto:", "tel:"]);
      if (href) {
        element.setAttribute("href", href);
        if (!href.startsWith("#")) {
          element.setAttribute("target", "_blank");
          element.setAttribute("rel", "noreferrer noopener");
        }
      }
      continue;
    }

    if (element.tagName === "IMG") {
      const src = safeUrl(originalAttributes.get("src"), baseUrl, ["http:", "https:"]);
      if (!src) {
        element.remove();
        continue;
      }
      element.setAttribute("src", src);
      const alt = originalAttributes.get("alt")?.trim();
      if (alt) element.setAttribute("alt", alt);
      continue;
    }

    if (element.tagName === "FIGURE" && originalAttributes.get("data-fp-type") === "image") {
      element.className = "editor-image";
      element.dataset.fpType = "image";
      continue;
    }

    if (element.tagName === "SPAN") {
      const className = (originalAttributes.get("class") ?? "")
        .split(/\s+/)
        .filter((value) => allowedInlineClasses.has(value))
        .join(" ");
      if (className) element.className = className;
      continue;
    }

    if (element.tagName === "PRE") {
      const language = originalAttributes.get("data-lang")?.trim().replace(/[^a-z0-9_+#.-]/gi, "").slice(0, 32);
      if (language) element.dataset.lang = language;
    }
  }

  return template.innerHTML;
}

function safeUrl(value: string | undefined, baseUrl: string, protocols: readonly string[]): string | null {
  const source = value?.trim();
  if (!source) return null;
  if (source.startsWith("#")) return source;

  try {
    const url = new URL(source, baseUrl);
    if (!protocols.includes(url.protocol)) return null;
    return source.startsWith("/") ? `${url.pathname}${url.search}${url.hash}` : url.toString();
  } catch {
    return null;
  }
}
