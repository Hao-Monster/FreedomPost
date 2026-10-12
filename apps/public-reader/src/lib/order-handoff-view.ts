import { type OrderHandoffController, type OrderHandoffRecord } from "./order-handoff.js";

export function orderHandoffDescription(record: OrderHandoffRecord): string {
  if (record.phase === "delivered") return "订单已发送给客服，并已在当前会话中显示。请等待客服确认后再完成后续交易。";
  if (record.phase === "action_required") {
    if (record.errorCode === "HANDOFF_CLOSED" || record.errorCode === "HANDOFF_CANCELLED") return record.order ? "客服交接已暂停，已生成的订单保留。点击下方按钮继续，不会重新下单。" : "下单请求已暂停。点击下方按钮核对原请求，不会另建订单。";
    if (["ORDER_SESSION_LOST", "ORDER_OWNER_REQUIRED", "ORDER_CSRF_INVALID"].includes(record.errorCode ?? "")) return record.creationAttempted ? "原下单会话已失效或无法验证。订单记录已保留，请恢复原浏览器会话后重试；不会自动另建订单。" : "浏览器未能确认安全下单会话。请允许本站 Cookie 后重试，目前尚未创建订单。";
    if (record.errorCode === "ORDER_RECOVERY_NOT_FOUND") return "暂时无法在当前浏览器会话找回原下单请求。为避免重复下单，已停止自动创建；请先联系人工客服核对。";
    if (record.errorCode === "SUPPORT_BINDING_MISMATCH") return "当前客服身份与订单绑定的会话不同。已停止发送，请恢复原客服会话后重试；不会将订单转发到其他会话。";
    if (record.errorCode === "SDK_UPGRADE_REQUIRED") return "订单已保留，客服组件尚未支持安全自动接单。请刷新页面后重试此订单。";
    if (record.errorCode === "RECOVERY_STORAGE_UNAVAILABLE") return "浏览器无法保存订单恢复记录，自动操作已暂停。请检查浏览器存储权限后重试原请求。";
    if (record.errorCode === "INVALID_RECOMMENDER") return "推荐人微信号不存在或已停用，未创建订单。请重新填写下单信息。";
    if (record.errorCode === "PRODUCT_UNAVAILABLE") return "商品或推荐人当前不可用，未创建订单。请刷新商品信息后重新填写。";
    if (record.errorCode === "INVALID_ORDER" || record.errorCode === "INVALID_PRICING") return "下单信息或商品定价未通过验证，未创建订单。请核对后重新填写。";
    if (record.supportDelivery?.status === "sent") return "订单消息已保存，但尚未确认在当前会话显示。点击下方按钮同步查看，不会重复发送。";
    return record.order ? "订单已登记，暂未确认送达客服。已生成的订单保留，重试发送此订单不会重新下单。" : "暂未确认下单结果，原请求已保留。请恢复此请求核对结果，避免重复下单。";
  }
  if (record.phase === "syncing" || (record.phase === "retry_wait" && record.supportDelivery?.status === "sent")) return "订单消息已保存，正在同步显示……";
  if (record.phase === "retry_wait") return record.order ? "订单已登记，暂未确认送达客服，正在重试……" : "正在核对原下单请求，请勿重复下单……";
  if (record.phase === "connecting") return "订单已登记，正在连接客服……";
  if (record.phase === "sending") return "正在发送订单信息……";
  if (record.phase === "creating") return "正在生成订单号，请勿重复下单……";
  if (record.phase === "checking") return "正在确认安全下单会话……";
  return record.order ? "已保存订单记录，准备恢复客服交接。" : "已保存下单请求，准备核对订单。";
}

export function orderHandoffErrorMessage(reason: unknown): string {
  const code = reason instanceof Error ? reason.message : "";
  if (code === "RECOVERY_STORAGE_UNAVAILABLE" || code === "RECOVERY_DATA_INVALID") return "浏览器无法安全保存下单恢复记录，尚未创建新订单。请检查本站存储权限后重试。";
  if (code === "TOO_MANY_PENDING_ORDERS") return "待处理订单过多。请先处理下方已有订单，再创建新订单。";
  if (code === "INVALID_ORDER") return "请填写有效的商品和推荐人微信号。";
  return "未能开始下单，请稍后重试。";
}

export function mountOrderHandoffView(root: HTMLElement, controller: OrderHandoffController, onOrderAvailable?: (record: OrderHandoffRecord) => void): () => void {
  const render = () => {
    const focused = root.contains(document.activeElement) ? document.activeElement as HTMLButtonElement : null;
    const focusedKey = focused?.dataset.orderRetry;
    const records = controller.list();
    const storageError = controller.getStorageError();
    root.hidden = records.length === 0 && !storageError;
    root.replaceChildren();
    if (root.hidden) return;
    const heading = document.createElement("h2");
    heading.textContent = "订单与客服交接";
    root.append(heading);
    if (storageError) {
      const warning = document.createElement("p");
      warning.setAttribute("role", "alert");
      warning.textContent = orderHandoffErrorMessage(new Error(storageError));
      root.append(warning);
    }
    for (const record of records) {
      const article = document.createElement("article");
      article.className = "market-order-handoff";
      article.dataset.phase = record.phase;
      article.dataset.orderRequest = record.requestKey;
      const title = document.createElement("h3");
      title.textContent = record.order?.productTitle ?? "待核对的下单请求";
      const code = document.createElement("p");
      code.className = "market-order-code";
      code.textContent = record.order ? `订单号：${record.order.orderCode}` : `商品：${record.input.productSlug}`;
      const status = document.createElement("p");
      status.className = "market-order-status";
      status.setAttribute("role", "status");
      status.textContent = orderHandoffDescription(record);
      const button = document.createElement("button");
      button.type = "button";
      button.className = "button secondary";
      button.dataset.orderRetry = record.requestKey;
      const busy = controller.isRunning(record.requestKey);
      button.disabled = busy;
      button.textContent = busy ? "正在处理此订单……" : record.phase === "delivered" ? "打开客服查看" : record.supportDelivery?.status === "sent" ? "同步查看订单消息" : record.order ? "重试发送此订单" : "恢复此下单请求";
      article.append(title, code, status, button);
      root.append(article);
      if (record.order && busy) onOrderAvailable?.(record);
    }
    if (focusedKey) root.querySelector<HTMLButtonElement>(`[data-order-retry="${focusedKey}"]`)?.focus({ preventScroll: true });
  };
  const onClick = (event: MouseEvent) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const button = target.closest<HTMLButtonElement>("[data-order-retry]");
    const requestKey = button?.dataset.orderRetry;
    if (!button || !root.contains(button) || button.disabled || !requestKey) return;
    void controller.run(requestKey);
  };
  root.addEventListener("click", onClick);
  const unsubscribe = controller.subscribe(render);
  return () => { unsubscribe(); root.removeEventListener("click", onClick); };
}
