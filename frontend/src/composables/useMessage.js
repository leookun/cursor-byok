import { inject, provide, reactive } from "vue";

const MESSAGE_API_SYMBOL = Symbol("message-api");
const MIN_VISIBLE_MS = 300;
let messageSeed = 0;

const messageState = reactive({
  current: null,
});

function clearMessageTimer(item) {
  if (item?.timer) {
    clearTimeout(item.timer);
    item.timer = null;
  }
}

function removeMessage(id, options = {}) {
  if (!messageState.current || messageState.current.id !== id) {
    return;
  }

  const current = messageState.current;
  const elapsed = Date.now() - current.shownAt;
  const force = options.force === true;
  if (!force && elapsed < MIN_VISIBLE_MS) {
    clearMessageTimer(current);
    current.timer = window.setTimeout(() => {
      removeMessage(id, { force: true });
    }, MIN_VISIBLE_MS - elapsed);
    return;
  }

  clearMessageTimer(current);
  messageState.current = null;
}

function showMessage(options = {}) {
  const type = typeof options.type === "string" ? options.type : "info";
  const content = String(options.content || "").trim();
  if (!content) {
    return null;
  }

  if (messageState.current) {
    clearMessageTimer(messageState.current);
  }

  const duration = Number.isFinite(options.duration)
    ? Math.max(0, options.duration)
    : type === "loading"
      ? 0
      : 2400;
  const id = `message-${Date.now()}-${messageSeed += 1}`;
  const item = {
    id,
    type,
    content,
    shownAt: Date.now(),
    timer: null,
  };

  if (duration > 0) {
    item.timer = window.setTimeout(() => {
      removeMessage(id);
    }, Math.max(duration, MIN_VISIBLE_MS));
  }

  messageState.current = item;
  return id;
}

export function createMessageApi() {
  return {
    state: messageState,
    show: showMessage,
    success(content, options = {}) {
      return showMessage({ ...options, type: "success", content });
    },
    error(content, options = {}) {
      return showMessage({ ...options, type: "error", content });
    },
    info(content, options = {}) {
      return showMessage({ ...options, type: "info", content });
    },
    loading(content, options = {}) {
      return showMessage({ ...options, type: "loading", content });
    },
    remove: removeMessage,
    clear() {
      if (messageState.current) {
        removeMessage(messageState.current.id, { force: true });
      }
    },
  };
}

const defaultMessageApi = createMessageApi();

export function provideMessage() {
  provide(MESSAGE_API_SYMBOL, defaultMessageApi);
  return defaultMessageApi;
}

export function useMessage() {
  return inject(MESSAGE_API_SYMBOL, defaultMessageApi);
}

export { messageState, showMessage, removeMessage };