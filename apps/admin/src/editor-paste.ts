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

export function pastedLinkUrl(text: string): string | null {
  const source = text.trim();
  if (!/^(?:https?:\/\/|mailto:|tel:)\S+$/i.test(source)) return null;

  try {
    const url = new URL(source);
    if (url.protocol === "http:" || url.protocol === "https:") return url.toString();
    return url.pathname ? source : null;
  } catch {
    return null;
  }
}

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

  normalizePastedBlocks(template.content);
  return template.innerHTML;
}

const pastedBlockTags = new Set([
  "BLOCKQUOTE",
  "DIV",
  "FIGURE",
  "H1",
  "H2",
  "H3",
  "H4",
  "H5",
  "H6",
  "OL",
  "P",
  "PRE",
  "UL"
]);

const pastedBlockSelector = "blockquote,div,figure,h1,h2,h3,h4,h5,h6,ol,p,pre,ul";

function normalizePastedBlocks(fragment: DocumentFragment): void {
  const containers = [...fragment.querySelectorAll<HTMLElement>("div,blockquote,p")].reverse();
  for (const element of containers) {
    if (element.closest("li")) {
      element.replaceWith(...element.childNodes);
      continue;
    }

    if (element.querySelector(pastedBlockSelector)) {
      wrapInlineRunsInContainer(element);
      element.replaceWith(...element.childNodes);
      continue;
    }

    if (element.tagName === "DIV" || element.tagName === "BLOCKQUOTE") {
      if (hasPastedInlineContent([...element.childNodes])) {
        const paragraph = document.createElement("p");
        paragraph.append(...element.childNodes);
        element.replaceWith(paragraph);
      } else {
        element.remove();
      }
    }
  }

  const hasTopLevelBlocks = [...fragment.children].some((child) => pastedBlockTags.has(child.tagName));
  const hasTopLevelLineBreak = [...fragment.childNodes].some(
    (node) => (node instanceof HTMLElement && node.tagName === "BR") || (node instanceof HTMLElement && !pastedBlockTags.has(node.tagName) && Boolean(node.querySelector("br")))
  );
  if (hasTopLevelBlocks || hasTopLevelLineBreak) {
    wrapInlineRunsInContainer(fragment);
  }

  for (const paragraph of [...fragment.querySelectorAll<HTMLParagraphElement>("p")]) {
    splitPastedParagraphOnLineBreaks(paragraph);
  }
}

function wrapInlineRunsInContainer(container: DocumentFragment | HTMLElement): void {
  let inlineRun: ChildNode[] = [];
  const flushInlineRun = (beforeNode: ChildNode | null) => {
    if (inlineRun.length === 0) return;
    if (hasPastedInlineContent(inlineRun)) {
      const paragraph = document.createElement("p");
      paragraph.append(...inlineRun);
      if (beforeNode) beforeNode.before(paragraph);
      else container.append(paragraph);
    } else {
      inlineRun.forEach((node) => node.remove());
    }
    inlineRun = [];
  };

  for (const child of [...container.childNodes]) {
    if (child instanceof HTMLElement && pastedBlockTags.has(child.tagName)) {
      flushInlineRun(child);
    } else {
      inlineRun.push(child);
    }
  }
  flushInlineRun(null);
}

function splitPastedParagraphOnLineBreaks(paragraph: HTMLParagraphElement): void {
  const lineBreaks = [...paragraph.querySelectorAll("br")];
  if (lineBreaks.length === 0) return;

  for (const br of lineBreaks) {
    if (!paragraph.contains(br)) continue;
    const range = document.createRange();
    range.setStart(paragraph, 0);
    range.setEndBefore(br);
    const segment = range.extractContents();
    br.remove();
    pruneEmptyInlineNodes(segment);
    if (hasPastedInlineContent([...segment.childNodes])) {
      const lineParagraph = document.createElement("p");
      lineParagraph.append(segment);
      paragraph.before(lineParagraph);
    }
  }

  pruneEmptyInlineNodes(paragraph);
  if (!hasPastedInlineContent([...paragraph.childNodes])) {
    paragraph.remove();
  }
}

function pruneEmptyInlineNodes(root: DocumentFragment | HTMLElement): void {
  const inlineElements = [...root.querySelectorAll<HTMLElement>("a,b,code,del,em,i,s,span,strong,u")].reverse();
  for (const el of inlineElements) {
    if (!hasPastedInlineContent([...el.childNodes])) {
      el.remove();
    }
  }
}

function hasPastedInlineContent(nodes: readonly Node[]): boolean {
  return nodes.some((node) => {
    if (node.nodeType === Node.TEXT_NODE) return Boolean(node.textContent?.trim());
    if (!(node instanceof Element)) return false;
    if (node.tagName === "IMG" || node.tagName === "FIGURE") return true;
    return Boolean(node.textContent?.trim() || node.querySelector("img,figure"));
  });
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
