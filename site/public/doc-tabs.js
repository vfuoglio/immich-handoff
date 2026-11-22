// Turns each .doc-tabs (made from the ":::tabs" fence) into tabs: a row of buttons, one panel shown. Without script every
// panel stays visible under its label, which still reads.
(function () {
  document.querySelectorAll('.doc-tabs').forEach(function (box) {
    var panels = Array.prototype.slice.call(box.querySelectorAll(':scope > .doc-tab'));
    if (!panels.length) return;
    var bar = document.createElement('div');
    bar.className = 'doc-tabbar';
    bar.setAttribute('role', 'tablist');
    function show(i) {
      panels.forEach(function (p, j) { p.hidden = j !== i; });
      Array.prototype.forEach.call(bar.children, function (b, j) { b.setAttribute('aria-selected', j === i ? 'true' : 'false'); });
    }
    panels.forEach(function (p, i) {
      var b = document.createElement('button');
      b.type = 'button'; b.setAttribute('role', 'tab'); b.textContent = p.getAttribute('data-label');
      b.addEventListener('click', function () { show(i); });
      bar.appendChild(b);
    });
    box.insertBefore(bar, box.firstChild);
    box.classList.add('ready');
    show(0);
  });
})();
