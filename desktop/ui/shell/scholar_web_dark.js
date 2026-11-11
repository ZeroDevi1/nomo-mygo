(() => {
  if (document.contentType !== "text/html" && document.contentType !== "application/xhtml+xml") return;

  const preference = matchMedia("(prefers-color-scheme: dark)");
  const attr = "data-nomo-reader-invert";
  const preserve = "data-nomo-reader-preserve";
  const css = `
    @media screen and (prefers-color-scheme: dark) {
      :root[${attr}] {
        color-scheme: only light !important;
        filter: invert(1) hue-rotate(180deg) !important;
      }
      :root[${attr}="canvas"] { background-color: #f3f3f3 !important; }
      :root[${attr}] [${preserve}],
      :root[${attr}] :is(img, video, canvas, iframe):not([${preserve}] *) {
        filter: hue-rotate(180deg) invert(1) !important;
      }
    }
  `;

  let installed = false;
  let pixel;
  let pending = false;
  let banner;

  function installStyle() {
    if (installed) return;
    installed = true;
    // A constructed sheet also works on sites that block inline style elements.
    if ("adoptedStyleSheets" in document && "replaceSync" in CSSStyleSheet.prototype) {
      const sheet = new CSSStyleSheet();
      sheet.replaceSync(css);
      document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
    } else {
      const style = document.createElement("style");
      style.textContent = css;
      (document.head || document.documentElement).append(style);
    }
  }

  function color(value) {
    if (!pixel) {
      const canvas = document.createElement("canvas");
      canvas.width = canvas.height = 1;
      pixel = canvas.getContext("2d", { willReadFrequently: true });
    }
    if (!pixel) return [0, 0, 0, 0];
    pixel.clearRect(0, 0, 1, 1);
    pixel.fillStyle = "transparent";
    pixel.fillStyle = value;
    pixel.fillRect(0, 0, 1, 1);
    return pixel.getImageData(0, 0, 1, 1).data;
  }

  function luminanceOver([r, g, b, alpha], under) {
    const opacity = alpha / 255;
    return opacity * (0.2126 * r + 0.7152 * g + 0.0722 * b) + (1 - opacity) * under;
  }

  function update() {
    const root = document.documentElement;
    const body = document.body;
    if (!root || !body) return;
    // Measure the site's own colors before forcing light native form controls.
    root.removeAttribute(attr);
    if (banner) {
      banner.removeAttribute(preserve);
      banner = null;
    }
    if (!preference.matches) return;
    const rootStyle = getComputedStyle(root);
    const rootColor = color(rootStyle.backgroundColor);
    const bodyColor = color(getComputedStyle(body).backgroundColor);
    const canvas = rootStyle.colorScheme.split(/\s+/).includes("dark") ? 18 : 255;
    if (luminanceOver(bodyColor, luminanceOver(rootColor, canvas)) < 128) return;
    installStyle();
    // A light article can have an already-dark masthead. Preserve a single
    // top-level banner and skip a second correction for its media.
    const header = document.querySelector('body > :is(header, [role="banner"]), body > * > :is(header, [role="banner"])');
    if (header && luminanceOver(color(getComputedStyle(header).backgroundColor), 255) < 128) {
      banner = header;
      banner.setAttribute(preserve, "");
    }
    root.setAttribute(attr, rootColor[3] === 0 ? "canvas" : "page");
  }

  function schedule() {
    if (pending) return;
    pending = true;
    requestAnimationFrame(() => {
      pending = false;
      update();
    });
  }

  function start() {
    // Theme classes and data attributes can change after hydration. Observe
    // only the two theme containers, without polling or scanning the document.
    const observer = new MutationObserver(schedule);
    const options = { attributeFilter: ["class", "style", "data-theme", "data-color-mode", "data-color-scheme", "data-bs-theme"] };
    observer.observe(document.documentElement, options);
    if (document.body) observer.observe(document.body, options);
    preference.addEventListener("change", schedule);
    window.addEventListener("load", schedule, { once: true });
    window.addEventListener("pageshow", schedule);
    schedule();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start, { once: true });
  } else {
    start();
  }
})();
