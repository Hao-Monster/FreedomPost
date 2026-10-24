type DomPoint = { path: number[]; offset: number };
type EditorSnapshot = {
  html: string;
  selection: { start: DomPoint; end: DomPoint } | null;
};

/** Presentation state must not create undo steps or invalidate redo. */
export function editorHistoryHtml(root: HTMLElement): string {
  if (!root.querySelector('.is-retrying,[data-image-import-action][disabled],[data-callout-emoji-picker]:not([hidden]),[data-callout-emoji-trigger][aria-expanded="true"]')) return root.innerHTML;
  const copy = root.cloneNode(true) as HTMLElement;
  for (const card of copy.querySelectorAll<HTMLElement>('[data-fp-type="image-import-error"]')) {
    card.classList.remove("is-retrying");
    card.querySelectorAll<HTMLButtonElement>("[data-image-import-action]").forEach((button) => { button.disabled = false; });
  }
  copy.querySelectorAll<HTMLElement>("[data-callout-emoji-picker]").forEach((picker) => { picker.hidden = true; });
  copy.querySelectorAll<HTMLElement>("[data-callout-emoji-trigger]").forEach((trigger) => { trigger.setAttribute("aria-expanded", "false"); });
  return copy.innerHTML;
}

/** Settle an async media node in history without resurrecting its pending state. */
export function replaceEditorSnapshotNode(snapshot: EditorSnapshot, selector: string, html: string, liveRoot?: HTMLElement, settledPendingID?: string, restorationOwnerID?: string): EditorSnapshot {
  const root = liveRoot ?? document.createElement("div");
  if (!liveRoot) root.innerHTML = snapshot.html;
  const target = root.querySelector(selector);
  const pendingID = settledPendingID ?? (target instanceof HTMLElement ? target.dataset.pendingMediaId : undefined);
  const splitPrefix = pendingID ? `${pendingID}-` : "";
  // A later upload can temporarily replace this slot as part of its selection.
  // If this upload fails while hidden, its outer split boundaries must remain
  // restorable by that enclosing upload, rather than by an already ended task.
  restorationOwnerID ??= html === "" && !target ? findRestorationOwner(root, selector) : undefined;
  for (const placeholder of root.querySelectorAll<HTMLElement>("[data-pending-restoration]")) {
    if (placeholder === target) continue;
    placeholder.dataset.pendingRestoration = replaceEditorSnapshotNode(
      { html: placeholder.dataset.pendingRestoration!, selection: null }, selector, html,
      undefined, pendingID, restorationOwnerID
    ).html;
  }
  const clearSettledSplits = () => {
    if (!splitPrefix) return;
    const clear = (fragment: ParentNode) => {
      for (const node of fragment.querySelectorAll<HTMLElement>("[data-editor-split-id]")) {
        const ids = node.dataset.editorSplitId!.split(/\s+/).flatMap((id) =>
          !id ? [] : !id.startsWith(splitPrefix) ? [id] : restorationOwnerID ? [`${restorationOwnerID}-restored-${id}`] : []);
        if (ids.length) node.dataset.editorSplitId = ids.join(" ");
        else delete node.dataset.editorSplitId;
      }
      for (const placeholder of fragment.querySelectorAll<HTMLElement>("[data-pending-restoration]")) {
        const restoration = document.createElement("template");
        restoration.innerHTML = placeholder.dataset.pendingRestoration!;
        clear(restoration.content);
        placeholder.dataset.pendingRestoration = restoration.innerHTML;
      }
    };
    clear(root);
  };
  if (!target?.parentNode) {
    clearSettledSplits();
    return { ...snapshot, html: editorHistoryHtml(root) };
  }

  const parentPath: number[] = [];
  for (let node: Node = target.parentNode; node !== root && node.parentNode; node = node.parentNode) {
    parentPath.unshift([...node.parentNode.childNodes].findIndex((child) => child === node));
  }
  const index = [...target.parentNode.childNodes].indexOf(target);
  const replacement = document.createElement("template");
  const restoreSplit = html === "" && target instanceof HTMLElement && target.dataset.fpType === "pending-media";
  replacement.innerHTML = restoreSplit ? target.dataset.pendingRestoration ?? "" : html;
  const count = replacement.content.childNodes.length;
  const movePoint = (point: DomPoint): DomPoint => {
    if (!parentPath.every((part, depth) => point.path[depth] === part)) return point;
    if (point.path.length === parentPath.length) {
      return point.offset > index ? { ...point, offset: point.offset + count - 1 } : point;
    }
    const childIndex = point.path[parentPath.length]!;
    if (childIndex === index) return { path: parentPath, offset: index + count };
    if (childIndex < index) return point;
    const path = [...point.path];
    path[parentPath.length] = childIndex + count - 1;
    return { ...point, path };
  };
  let selection = snapshot.selection && {
    start: movePoint(snapshot.selection.start),
    end: movePoint(snapshot.selection.end)
  };
  const left = target.previousSibling;
  const right = target.nextSibling;
  const inserted = [...replacement.content.childNodes];
  target.replaceWith(replacement.content);

  const pathOf = (node: Node): number[] => {
    const path: number[] = [];
    for (let current = node; current !== root && current.parentNode; current = current.parentNode) {
      path.unshift([...current.parentNode.childNodes].findIndex((child) => child === current));
    }
    return path;
  };
  const mergeBoundary = (before: Node | null, after: Node | null): Node | null => {
    if (!before || !after || before.nextSibling !== after) return after;
    const text = before.nodeType === Node.TEXT_NODE && after.nodeType === Node.TEXT_NODE;
    const split = before instanceof HTMLElement && after instanceof HTMLElement &&
      before.tagName === after.tagName && splitPrefix &&
      (before.dataset.editorSplitId ?? "").split(/\s+/).some((id) =>
        id.startsWith(splitPrefix) && (after.dataset.editorSplitId ?? "").split(/\s+/).includes(id));
    if (!text && !split) return after;

    const beforePath = pathOf(before);
    const afterPath = pathOf(after);
    const parentPath = afterPath.slice(0, -1);
    const index = afterPath.at(-1)!;
    const offset = text ? (before.textContent?.length ?? 0) : before.childNodes.length;
    const move = (point: DomPoint): DomPoint => {
      if (!parentPath.every((part, depth) => point.path[depth] === part)) return point;
      if (point.path.length === parentPath.length) {
        if (point.offset === index) return { path: beforePath, offset };
        return point.offset > index ? { ...point, offset: point.offset - 1 } : point;
      }
      const childIndex = point.path[parentPath.length]!;
      if (childIndex === index) {
        if (point.path.length === afterPath.length) return { path: beforePath, offset: offset + point.offset };
        const rest = point.path.slice(afterPath.length);
        rest[0] = rest[0]! + offset;
        return { ...point, path: [...beforePath, ...rest] };
      }
      if (childIndex < index) return point;
      const path = [...point.path];
      path[parentPath.length] = childIndex - 1;
      return { ...point, path };
    };
    if (selection) selection = { start: move(selection.start), end: move(selection.end) };
    if (text) {
      (before as Text).appendData(after.textContent ?? "");
      after.parentNode?.removeChild(after);
    } else {
      (before as HTMLElement).dataset.editorSplitId = [...new Set([
        ...(before as HTMLElement).dataset.editorSplitId!.split(/\s+/),
        ...(after as HTMLElement).dataset.editorSplitId!.split(/\s+/)
      ])].join(" ");
      const end = before.lastChild;
      const start = after.firstChild;
      while (after.firstChild) before.appendChild(after.firstChild);
      after.parentNode?.removeChild(after);
      mergeBoundary(end, start);
    }
    return before;
  };
  if (restoreSplit) {
    if (inserted.length === 0) mergeBoundary(left, right);
    else {
      const first = mergeBoundary(left, inserted[0]!);
      mergeBoundary(inserted.length === 1 ? first : inserted.at(-1)!, right);
    }
  }
  clearSettledSplits();
  return { html: editorHistoryHtml(root), selection };
}

function findRestorationOwner(root: HTMLElement, selector: string): string | undefined {
  for (const placeholder of root.querySelectorAll<HTMLElement>("[data-pending-restoration]")) {
    const restoration = document.createElement("div");
    restoration.innerHTML = placeholder.dataset.pendingRestoration!;
    if (restoration.querySelector(selector)) return placeholder.dataset.pendingMediaId;
    const owner = findRestorationOwner(restoration, selector);
    if (owner) return owner;
  }
  return undefined;
}
