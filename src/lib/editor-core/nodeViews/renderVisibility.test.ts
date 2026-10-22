import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { observeRenderVisibility } from './renderVisibility';

class FakeIntersectionObserver {
  static instances: FakeIntersectionObserver[] = [];
  readonly observed = new Set<Element>();
  disconnected = false;

  constructor(readonly callback: IntersectionObserverCallback) {
    FakeIntersectionObserver.instances.push(this);
  }

  observe(element: Element) {
    this.observed.add(element);
  }

  unobserve(element: Element) {
    this.observed.delete(element);
  }

  disconnect() {
    this.disconnected = true;
    this.observed.clear();
  }

  emit(target: Element, isIntersecting: boolean) {
    this.callback([{ target, isIntersecting } as IntersectionObserverEntry], this as unknown as IntersectionObserver);
  }
}

describe('observeRenderVisibility', () => {
  beforeEach(() => {
    FakeIntersectionObserver.instances = [];
    vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('同一文档的多个增强块共用一个观察器，并按元素分发可见性', () => {
    const first = document.createElement('div');
    const second = document.createElement('div');
    const firstChanges: boolean[] = [];
    const secondChanges: boolean[] = [];
    const stopFirst = observeRenderVisibility(first, (visible) => firstChanges.push(visible));
    const stopSecond = observeRenderVisibility(second, (visible) => secondChanges.push(visible));

    expect(FakeIntersectionObserver.instances).toHaveLength(1);
    const [observer] = FakeIntersectionObserver.instances;
    observer.emit(second, true);
    expect(firstChanges).toEqual([]);
    expect(secondChanges).toEqual([true]);

    stopFirst();
    stopSecond();
  });

  it('最后一个块取消观察时释放观察器和文档可见性监听，之后可重新创建', () => {
    const removeListener = vi.spyOn(document, 'removeEventListener');
    const first = document.createElement('div');
    const second = document.createElement('div');
    const stopFirst = observeRenderVisibility(first, () => undefined);
    const stopSecond = observeRenderVisibility(second, () => undefined);
    const [observer] = FakeIntersectionObserver.instances;

    stopFirst();
    stopFirst();
    expect(observer.disconnected).toBe(false);
    expect(observer.observed.has(second)).toBe(true);

    stopSecond();
    expect(observer.disconnected).toBe(true);
    expect(removeListener).toHaveBeenCalledWith('visibilitychange', expect.any(Function));

    const stopAgain = observeRenderVisibility(first, () => undefined);
    expect(FakeIntersectionObserver.instances).toHaveLength(2);
    stopAgain();
    removeListener.mockRestore();
  });

  it('文档可见性变化时通知所有接近视口的块', () => {
    const element = document.createElement('div');
    const changes: boolean[] = [];
    const stop = observeRenderVisibility(element, (visible) => changes.push(visible));
    FakeIntersectionObserver.instances[0].emit(element, true);
    document.dispatchEvent(new Event('visibilitychange'));
    expect(changes).toEqual([true, true]);
    stop();
  });
});
