/* Draws the ```diagram fences. Mermaid is vendored (assets/vendor/mermaid.min.js,
   MIT) and loaded only on a page that has a diagram, so nothing is fetched from
   a third party at runtime. Each diagram is drawn at its natural size in a
   container that scrolls sideways, with a "View full size" overlay that pans
   and zooms. Esc closes it, Tab stays inside it, and focus returns on close. */
(function () {
  var base = document.currentScript.src.replace(/[^/]*$/, "");
  var nodes = [];

  function scheme() {
    return document.body.getAttribute("data-md-color-scheme") === "slate" ? "dark" : "default";
  }

  function load(cb) {
    if (window.mermaid) return cb();
    var s = document.createElement("script");
    s.src = base + "vendor/mermaid.min.js";
    s.onload = cb;
    document.head.appendChild(s);
  }

  function openOverlay(svgSource, opener) {
    var overlay = document.createElement("div");
    overlay.className = "diagram-overlay";
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-label", "Diagram, full size");
    var bar = document.createElement("div");
    bar.className = "diagram-bar";
    var stage = document.createElement("div");
    stage.className = "diagram-stage";
    stage.tabIndex = 0;
    function btn(label, text, fn) {
      var b = document.createElement("button");
      b.type = "button"; b.textContent = text; b.setAttribute("aria-label", label); b.onclick = fn;
      bar.appendChild(b); return b;
    }
    var hint = document.createElement("span");
    hint.textContent = "Drag to pan, scroll or + / - to zoom, 0 to fit, Esc to close";
    var svg = svgSource.cloneNode(true);
    svg.removeAttribute("style");
    stage.appendChild(svg);
    var w = parseFloat(svg.getAttribute("width")) || svg.viewBox.baseVal.width || 800;
    var h = parseFloat(svg.getAttribute("height")) || svg.viewBox.baseVal.height || 600;
    svg.setAttribute("width", w); svg.setAttribute("height", h);
    var k = 1, x = 0, y = 0;
    function apply() { svg.style.transform = "translate(" + x + "px," + y + "px) scale(" + k + ")"; }
    function fit() {
      var r = stage.getBoundingClientRect();
      k = Math.min(r.width / w, r.height / h, 3) * 0.95;
      x = (r.width - w * k) / 2; y = (r.height - h * k) / 2; apply();
    }
    function zoom(f, cx, cy) {
      var r = stage.getBoundingClientRect();
      cx = cx == null ? r.width / 2 : cx; cy = cy == null ? r.height / 2 : cy;
      var nk = Math.max(0.1, Math.min(10, k * f)); f = nk / k;
      x = cx - (cx - x) * f; y = cy - (cy - y) * f; k = nk; apply();
    }
    btn("Zoom in", "+", function () { zoom(1.25); });
    btn("Zoom out", "−", function () { zoom(0.8); });
    btn("Fit to screen", "Fit", fit);
    var close = btn("Close", "Close", done);
    bar.appendChild(hint);
    overlay.appendChild(bar); overlay.appendChild(stage);
    document.body.appendChild(overlay);
    var prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    fit(); close.focus();

    stage.addEventListener("wheel", function (e) {
      e.preventDefault();
      var r = stage.getBoundingClientRect();
      zoom(e.deltaY < 0 ? 1.15 : 1 / 1.15, e.clientX - r.left, e.clientY - r.top);
    }, { passive: false });
    var drag = null;
    stage.addEventListener("pointerdown", function (e) {
      drag = { x: e.clientX - x, y: e.clientY - y };
      stage.classList.add("dragging"); stage.setPointerCapture(e.pointerId);
    });
    stage.addEventListener("pointermove", function (e) {
      if (drag) { x = e.clientX - drag.x; y = e.clientY - drag.y; apply(); }
    });
    function up() { drag = null; stage.classList.remove("dragging"); }
    stage.addEventListener("pointerup", up); stage.addEventListener("pointercancel", up);

    function key(e) {
      if (e.key === "Escape") { e.preventDefault(); return done(); }
      if (e.key === "Tab") {
        var f = overlay.querySelectorAll("button, [tabindex='0']");
        var first = f[0], last = f[f.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
        return;
      }
      if (document.activeElement && document.activeElement.tagName === "BUTTON" && (e.key === "Enter" || e.key === " ")) return;
      if (e.key === "+" || e.key === "=") zoom(1.25);
      else if (e.key === "-") zoom(0.8);
      else if (e.key === "0") fit();
      else if (e.key === "ArrowLeft") { x += 60; apply(); }
      else if (e.key === "ArrowRight") { x -= 60; apply(); }
      else if (e.key === "ArrowUp") { y += 60; apply(); }
      else if (e.key === "ArrowDown") { y -= 60; apply(); }
      else return;
      e.preventDefault();
    }
    document.addEventListener("keydown", key, true);
    function done() {
      document.removeEventListener("keydown", key, true);
      document.body.style.overflow = prevOverflow;
      overlay.remove();
      if (opener) opener.focus();
    }
  }

  function draw() {
    var pres = document.querySelectorAll("pre.diagram-source");
    if (!pres.length) return;
    load(function () {
      window.mermaid.initialize({
        startOnLoad: false,
        theme: scheme(),
        securityLevel: "strict",
        flowchart: { useMaxWidth: false, wrappingWidth: 150, nodeSpacing: 20, rankSpacing: 36, padding: 8, curve: "basis" },
        sequence: { useMaxWidth: false, wrap: true, width: 110, actorMargin: 18, messageMargin: 36, boxMargin: 6, noteMargin: 6, messageFontSize: 14, actorFontSize: 14 },
        themeVariables: { fontSize: "14px" }
      });
      var n = 0;
      pres.forEach(function (pre) {
        var src = pre.textContent;
        var box = document.createElement("figure");
        box.className = "diagram"; box.style.margin = "1em 0";
        var scroll = document.createElement("div");
        scroll.className = "diagram-scroll"; scroll.tabIndex = 0;
        scroll.setAttribute("role", "group"); scroll.setAttribute("aria-label", "Diagram");
        box.appendChild(scroll);
        pre.replaceWith(box);
        window.mermaid.render("sluis-diagram-" + (n++), src).then(function (r) {
          scroll.innerHTML = r.svg;
          var svg = scroll.querySelector("svg");
          var open = document.createElement("button");
          open.type = "button"; open.className = "diagram-open"; open.textContent = "View full size";
          open.onclick = function () { openOverlay(svg, open); };
          box.insertBefore(open, scroll);
        }).catch(function (e) {
          scroll.className += " diagram-fallback"; scroll.textContent = src;
          console.error("diagram", e);
        });
      });
    });
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", draw); else draw();
})();
