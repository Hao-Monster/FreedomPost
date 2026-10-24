/**
 * Inserts a block placeholder at the current editor selection.
 *
 * Rich HTML copied from sites such as WeChat can wrap the whole article in
 * nested divs. The placeholder must stay next to the nearest editable block,
 * rather than after the editor's outermost wrapper.
 */
export function insertBlockPlaceholderAtSelection(
  editor: HTMLElement,
  placeholder: HTMLElement,
  selection: Selection | null
): void {
  if (selection?.rangeCount) {
    const range = selection.getRangeAt(0);
    if (!editor.contains(range.commonAncestorContainer)) {
      editor.append(placeholder);
      return;
    }

    const container = closestCalloutContent(range.startContainer, editor) ?? editor;
    if (placeholder.dataset.fpType === "pending-media") {
      // Range extraction clones the boundary ancestors. Retain their identities
      // so a failed upload can join only those split boundaries, including lists.
      let part = 0;
      const prefix = `${placeholder.dataset.pendingMediaId}-`;
      for (const boundary of [range.startContainer, range.endContainer]) {
        for (let node = boundary instanceof Element ? boundary : boundary.parentElement; node && node !== container; node = node.parentElement) {
          if (node instanceof HTMLElement) {
            const ids = (node.dataset.editorSplitId ?? "").split(/\s+/).filter(Boolean);
            if (!ids.some((id) => id.startsWith(prefix))) ids.push(`${prefix}${part++}`);
            node.dataset.editorSplitId = ids.join(" ");
          }
        }
      }
      if (!range.collapsed) {
        const original = document.createElement("template");
        let restored = range.cloneContents();
        const block = closestSplittableBlock(range.startContainer, container);
        if (block?.contains(range.commonAncestorContainer)) {
          for (let node = range.commonAncestorContainer instanceof Element ? range.commonAncestorContainer : range.commonAncestorContainer.parentElement; node; node = node.parentElement) {
            const wrapper = node.cloneNode(false);
            wrapper.appendChild(restored);
            restored = document.createDocumentFragment();
            restored.append(wrapper);
            if (node === block) break;
          }
        }
        original.content.append(restored);
        placeholder.dataset.pendingRestoration = original.innerHTML;
      }
    }
    range.deleteContents();

    const block = closestSplittableBlock(range.startContainer, container);
    if (block) insertBetweenSplitBlock(range, block, placeholder);
    else range.insertNode(placeholder);
    return;
  }

  editor.append(placeholder);
}

/** Focuses the editor and creates a collapsed caret at its first editable position. */
export function focusEditorStart(editor: HTMLElement, selection: Selection | null = window.getSelection()): Range | null {
  editor.focus();
  if (!selection) return null;

  const range = document.createRange();
  range.selectNodeContents(editor.firstChild ?? editor);
  range.collapse(true);
  selection.removeAllRanges();
  selection.addRange(range);
  return range;
}

function closestSplittableBlock(node: Node, container: HTMLElement): HTMLElement | null {
  const element = node instanceof Element ? node : node.parentElement;
  // Block insertions must split the enclosing list, not leave paragraphs or
  // media inside a list item (or directly under ul/ol), where saving loses them.
  let list: HTMLElement | null = null;
  for (let ancestor = element; ancestor && ancestor !== container && container.contains(ancestor); ancestor = ancestor.parentElement) {
    if (ancestor instanceof HTMLElement && ancestor.matches("ul,ol")) list = ancestor;
  }
  if (list) return list;

  const block = element?.closest<HTMLElement>("p,h1,h2,h3,h4,h5,h6,div,blockquote,pre") ?? null;
  return block && block !== container && container.contains(block) ? block : null;
}

function insertBetweenSplitBlock(range: Range, block: HTMLElement, placeholder: HTMLElement): void {
  const tailRange = document.createRange();
  tailRange.setStart(range.startContainer, range.startOffset);
  tailRange.setEnd(block, block.childNodes.length);

  const trailingBlock = block.cloneNode(false) as HTMLElement;
  trailingBlock.append(tailRange.extractContents());
  block.after(placeholder);

  if (hasMeaningfulContent(trailingBlock)) placeholder.after(trailingBlock);
  if (!hasMeaningfulContent(block)) block.remove();
}

function hasMeaningfulContent(element: HTMLElement): boolean {
  if (element.textContent?.trim()) return true;
  return Boolean(element.querySelector("img,figure,video,iframe,.editor-attachment"));
}

function closestCalloutContent(node: Node | null, editor: HTMLElement): HTMLElement | null {
  const element = node instanceof Element ? node : node?.parentElement;
  const content = element?.closest<HTMLElement>(".editor-callout-content") ?? null;
  return content && editor.contains(content) ? content : null;
}

const editorBlockSelector =
  'p,h1,h2,h3,h4,h5,h6,ul,ol,pre,blockquote,div,figure,[data-fp-type="callout"],.editor-attachment,[data-fp-type="image-import-error"],[data-fp-type="pending-media"]';

const atomicBlockSelector =
  '[data-fp-type="callout"],figure,pre,ul,ol,.editor-attachment,[data-fp-type="image-import-error"],[data-fp-type="pending-media"]';

export function resolveCalloutTargetBlocks(
  editor: HTMLElement,
  selection: Selection | null
): { selectedNodes: ChildNode[]; collapsedInsertBefore: ChildNode | null } {
  if (!selection?.rangeCount || !editor.contains(selection.anchorNode)) {
    return { selectedNodes: [], collapsedInsertBefore: null };
  }

  const range = selection.getRangeAt(0);
  if (!editor.contains(range.commonAncestorContainer)) {
    return { selectedNodes: [], collapsedInsertBefore: null };
  }

  const wasCollapsed = range.collapsed;
  const startMarker = document.createElement("span");
  startMarker.dataset.fpSelectionMarker = "start";
  let endMarker = startMarker;

  if (wasCollapsed) {
    const caretRange = range.cloneRange();
    caretRange.collapse(true);
    caretRange.insertNode(startMarker);
  } else {
    endMarker = document.createElement("span");
    endMarker.dataset.fpSelectionMarker = "end";
    const endRange = range.cloneRange();
    endRange.collapse(false);
    endRange.insertNode(endMarker);
    const startRange = range.cloneRange();
    startRange.collapse(true);
    startRange.insertNode(startMarker);
  }

  const resolveStartBlock = (): ChildNode | null =>
    startMarker.parentNode === editor
      ? wasCollapsed
        ? null
        : startMarker.nextSibling
      : closestDirectChild(startMarker, editor);

  const resolveEndBlock = (fallback: ChildNode | null): ChildNode | null =>
    endMarker.parentNode === editor
      ? wasCollapsed
        ? null
        : endMarker.previousSibling
      : closestDirectChild(endMarker, editor) ?? fallback;

  try {
    normalizeEditorContainerBlocks(editor);

    if (wasCollapsed && startMarker.parentNode === editor) {
      const insertBefore = startMarker.nextSibling;
      return { selectedNodes: [], collapsedInsertBefore: insertBefore };
    }

    let startBlock = resolveStartBlock();
    let endBlock = resolveEndBlock(startBlock);
    const initialChildren = [...editor.childNodes];
    const initialStartIndex = startBlock ? initialChildren.indexOf(startBlock) : -1;
    const initialEndIndex = endBlock ? initialChildren.indexOf(endBlock) : initialStartIndex;
    const initialSlice =
      initialStartIndex >= 0 && initialEndIndex >= initialStartIndex
        ? initialChildren.slice(initialStartIndex, initialEndIndex + 1)
        : [];

    if (
      initialSlice.some(
        (node) =>
          node instanceof Element &&
          (node.matches('[data-fp-type="callout"]') || Boolean(node.querySelector('[data-fp-type="callout"]')))
      )
    ) {
      return { selectedNodes: initialSlice, collapsedInsertBefore: null };
    }

    for (const node of initialSlice) {
      if (node instanceof HTMLParagraphElement && node.querySelector("br") && hasMeaningfulContent(node)) {
        splitParagraphAtLineBreaksWithMarkers(node);
      }
    }

    startBlock = resolveStartBlock();
    endBlock = resolveEndBlock(startBlock);

    if (!wasCollapsed && startBlock && endBlock) {
      if (startBlock !== endBlock) {
        if (
          startBlock instanceof HTMLElement &&
          startBlock.contains(startMarker) &&
          !hasContentAfterMarker(startBlock, startMarker) &&
          startBlock.nextSibling
        ) {
          startBlock = startBlock.nextSibling as ChildNode;
        }
        if (
          endBlock instanceof HTMLElement &&
          endBlock.contains(endMarker) &&
          !hasContentBeforeMarker(endBlock, endMarker) &&
          endBlock.previousSibling
        ) {
          endBlock = endBlock.previousSibling as ChildNode;
        }
      }

      if (startBlock instanceof HTMLParagraphElement && startBlock.contains(startMarker)) {
        const beforeRange = document.createRange();
        beforeRange.setStart(startBlock, 0);
        beforeRange.setEndBefore(startMarker);
        if (hasFragmentContent(beforeRange.cloneContents())) {
          const leadingFragment = beforeRange.extractContents();
          pruneEmptyInlineWrappers(leadingFragment);
          const leadingParagraph = document.createElement("p");
          leadingParagraph.append(leadingFragment);
          startBlock.before(leadingParagraph);
          pruneEmptyInlineWrappers(startBlock);
        }
      }

      if (endBlock instanceof HTMLParagraphElement && endBlock.contains(endMarker)) {
        const afterRange = document.createRange();
        afterRange.setStartAfter(endMarker);
        afterRange.setEnd(endBlock, endBlock.childNodes.length);
        if (hasFragmentContent(afterRange.cloneContents())) {
          const trailingFragment = afterRange.extractContents();
          pruneEmptyInlineWrappers(trailingFragment);
          const trailingParagraph = document.createElement("p");
          trailingParagraph.append(trailingFragment);
          endBlock.after(trailingParagraph);
          pruneEmptyInlineWrappers(endBlock);
        }
      }
    }

    const finalStart = (startBlock?.isConnected ? startBlock : null) ?? resolveStartBlock();
    const finalEnd = (endBlock?.isConnected ? endBlock : null) ?? resolveEndBlock(finalStart);
    startMarker.remove();
    if (endMarker !== startMarker) endMarker.remove();

    const children = [...editor.childNodes];
    const startIndex = finalStart ? children.indexOf(finalStart) : -1;
    const endIndex = finalEnd ? children.indexOf(finalEnd) : startIndex;
    const selectedNodes = startIndex >= 0 && endIndex >= startIndex ? children.slice(startIndex, endIndex + 1) : [];

    for (const node of selectedNodes) {
      if (node instanceof HTMLParagraphElement && !hasMeaningfulContent(node) && !node.querySelector("br")) {
        node.append(document.createElement("br"));
      }
    }

    return { selectedNodes, collapsedInsertBefore: null };
  } finally {
    startMarker.remove();
    if (endMarker !== startMarker) endMarker.remove();
  }
}

function normalizeEditorContainerBlocks(editor: HTMLElement): void {
  const candidates = [...editor.querySelectorAll<HTMLElement>("div,blockquote,p")].reverse();
  for (const element of candidates) {
    if (element.closest("li")) {
      element.replaceWith(...element.childNodes);
      continue;
    }
    if (element.closest(atomicBlockSelector)) continue;
    if (element.matches(atomicBlockSelector)) continue;

    if (element.querySelector(editorBlockSelector)) {
      wrapInlineRunsWithMarkers(element);
      element.replaceWith(...element.childNodes);
      continue;
    }

    if (element.tagName === "DIV" || element.tagName === "BLOCKQUOTE") {
      const hasContent = hasMeaningfulContent(element);
      const hasMarker = Boolean(element.querySelector("[data-fp-selection-marker]"));
      if (hasContent || hasMarker) {
        const paragraph = document.createElement("p");
        paragraph.append(...element.childNodes);
        if (!hasContent && !paragraph.querySelector("br")) {
          paragraph.append(document.createElement("br"));
        }
        element.replaceWith(paragraph);
      } else {
        element.remove();
      }
    }
  }

  wrapInlineRunsWithMarkers(editor);
}

function wrapInlineRunsWithMarkers(container: HTMLElement): void {
  let inlineRun: ChildNode[] = [];
  const flushInlineRun = (beforeNode: ChildNode | null) => {
    if (inlineRun.length === 0) return;
    const hasContent = inlineRun.some((node) =>
      node.nodeType === Node.TEXT_NODE
        ? Boolean(node.textContent?.trim())
        : node instanceof HTMLElement &&
          !node.hasAttribute("data-fp-selection-marker") &&
          hasMeaningfulContent(node)
    );
    if (hasContent) {
      const paragraph = document.createElement("p");
      paragraph.append(...inlineRun);
      if (beforeNode) beforeNode.before(paragraph);
      else container.append(paragraph);
    } else {
      for (const node of inlineRun) {
        if (
          node instanceof HTMLElement &&
          (node.hasAttribute("data-fp-selection-marker") || Boolean(node.querySelector("[data-fp-selection-marker]")))
        ) {
          continue;
        }
        node.remove();
      }
    }
    inlineRun = [];
  };

  for (const child of [...container.childNodes]) {
    if (child instanceof HTMLElement && child.matches(editorBlockSelector)) {
      flushInlineRun(child);
    } else {
      inlineRun.push(child);
    }
  }
  flushInlineRun(null);
}

function splitParagraphAtLineBreaksWithMarkers(paragraph: HTMLParagraphElement): void {
  const lineBreaks = [...paragraph.querySelectorAll("br")];
  if (lineBreaks.length === 0) return;

  for (const br of lineBreaks) {
    if (!paragraph.contains(br)) continue;
    const range = document.createRange();
    range.setStart(paragraph, 0);
    range.setEndBefore(br);
    const segment = range.extractContents();
    br.remove();
    pruneEmptyInlineWrappers(segment);
    const hasContent = hasFragmentContent(segment);
    const hasMarker = Boolean(segment.querySelector("[data-fp-selection-marker]"));
    if (hasContent || hasMarker) {
      const lineParagraph = document.createElement("p");
      lineParagraph.append(segment);
      if (!hasContent && !lineParagraph.querySelector("br")) {
        lineParagraph.append(document.createElement("br"));
      }
      paragraph.before(lineParagraph);
    }
  }

  pruneEmptyInlineWrappers(paragraph);
  const remainingHasContent = hasMeaningfulContent(paragraph);
  const remainingHasMarker = Boolean(paragraph.querySelector("[data-fp-selection-marker]"));
  if (!remainingHasContent && !remainingHasMarker) {
    paragraph.remove();
  } else if (!remainingHasContent && !paragraph.querySelector("br")) {
    paragraph.append(document.createElement("br"));
  }
}

function pruneEmptyInlineWrappers(root: DocumentFragment | HTMLElement): void {
  const inlineElements = [...root.querySelectorAll<HTMLElement>("a,b,code,del,em,i,s,span,strong,u")].reverse();
  for (const el of inlineElements) {
    if (el.hasAttribute("data-fp-selection-marker") || el.querySelector("[data-fp-selection-marker]")) {
      continue;
    }
    if (!hasMeaningfulContent(el)) {
      el.remove();
    }
  }
}

function hasFragmentContent(fragment: DocumentFragment): boolean {
  if (fragment.textContent?.trim()) return true;
  return Boolean(fragment.querySelector("img,figure,video,iframe,.editor-attachment"));
}

function hasContentBeforeMarker(block: HTMLElement, marker: HTMLElement): boolean {
  if (!block.contains(marker)) return true;
  const range = document.createRange();
  range.setStart(block, 0);
  range.setEndBefore(marker);
  return hasFragmentContent(range.cloneContents());
}

function hasContentAfterMarker(block: HTMLElement, marker: HTMLElement): boolean {
  if (!block.contains(marker)) return true;
  const range = document.createRange();
  range.setStartAfter(marker);
  range.setEnd(block, block.childNodes.length);
  return hasFragmentContent(range.cloneContents());
}

function closestDirectChild(node: Node | null, container: HTMLElement): ChildNode | null {
  let current = node;
  while (current?.parentNode && current.parentNode !== container) {
    current = current.parentNode;
  }
  return current?.parentNode === container ? (current as ChildNode) : null;
}
