/**
 * 同一文档内所有增强块（代码高亮、Mermaid 图表）共用一个 IntersectionObserver 与一个
 * visibilitychange 监听，避免块数增多时观察器和监听线性增长。注册表在首个块观察时创建，
 * 最后一个块取消观察时断开并移除，不跨文档常驻。
 */
interface VisibilityEntry {
  nearViewport: boolean;
  onChange: (visible: boolean) => void;
}

interface VisibilityRegistry {
  observer: IntersectionObserver | null;
  entries: Map<Element, VisibilityEntry>;
  onDocumentVisibility: () => void;
}

/** 视口外预渲染的距离：提前增强即将滚入的块，滚动时不出现未高亮的闪烁。 */
const RENDER_ROOT_MARGIN = '400px';

const registries = new WeakMap<Document, VisibilityRegistry>();

function notify(document: Document, entry: VisibilityEntry) {
  entry.onChange(entry.nearViewport && !document.hidden);
}

function acquireRegistry(document: Document): VisibilityRegistry {
  const existing = registries.get(document);
  if (existing) return existing;
  const entries = new Map<Element, VisibilityEntry>();
  const observer = typeof IntersectionObserver === 'function'
    ? new IntersectionObserver((changes) => {
      for (const change of changes) {
        const entry = entries.get(change.target);
        if (!entry) continue;
        entry.nearViewport = change.isIntersecting;
        notify(document, entry);
      }
    }, { rootMargin: RENDER_ROOT_MARGIN }) : null;
  const registry: VisibilityRegistry = {
    observer,
    entries,
    onDocumentVisibility: () => {
      for (const entry of [...entries.values()]) notify(document, entry);
    },
  };
  document.addEventListener('visibilitychange', registry.onDocumentVisibility);
  registries.set(document, registry);
  return registry;
}

/**
 * 观察元素是否接近视口且文档可见；不可见文档暂停增强。无 IntersectionObserver 的导出／测试宿主
 * 视为始终接近视口，保持即时渲染。
 *
 * @param element 增强块根节点；同一元素重复观察时以最后一次注册为准。
 * @param onChange 可见性变化回调；有观察器时首次观察后由其初始回调触发一次，之后随视口与
 *   文档可见性变化触发；注册时不会同步调用。
 * @returns 取消观察函数，幂等；最后一个元素取消时释放共享观察器与监听。
 */
export function observeRenderVisibility(element: HTMLElement, onChange: (visible: boolean) => void): () => void {
  const document = element.ownerDocument;
  const registry = acquireRegistry(document);
  const entry: VisibilityEntry = { nearViewport: registry.observer === null, onChange };
  registry.entries.set(element, entry);
  registry.observer?.observe(element);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    // 元素被重新注册过时，只释放属于本次注册的条目。
    if (registry.entries.get(element) !== entry) return;
    registry.entries.delete(element);
    registry.observer?.unobserve(element);
    if (registry.entries.size > 0) return;
    registry.observer?.disconnect();
    document.removeEventListener('visibilitychange', registry.onDocumentVisibility);
    registries.delete(document);
  };
}

/** 屏幕上方的占位换成图表时保持阅读位置；仅在增强结果提交时测量一次。 */
export function preserveRenderScrollAnchor(element: HTMLElement, update: () => void): void {
  const pane = element.closest<HTMLElement>('.semantic-pane');
  const before = pane && element.getBoundingClientRect();
  const above = pane && before && before.bottom <= pane.getBoundingClientRect().top;
  const scrollTop = pane?.scrollTop ?? 0;
  update();
  if (above && pane && before) pane.scrollTop = scrollTop + element.getBoundingClientRect().height - before.height;
}
