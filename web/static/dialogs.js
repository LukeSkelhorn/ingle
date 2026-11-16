// Admin pop-ups. A link with data-dialog="id" opens that <dialog> as a
// modal; without JavaScript the link loads the page with it open (?edit=).
// A dialog the page comes with open (data-open) opens as a modal too.
// Escape, a tap outside it, or a [data-dialog-close] link closes it, and
// closing takes ?edit= off the address.
(function () {
  function clean() {
    if (!/[?&]edit=/.test(location.search)) return;
    history.replaceState(null, "", location.pathname + location.hash);
  }
  function wire(d) {
    if (d.dataset.wired) return;
    d.dataset.wired = "1";
    d.addEventListener("click", function (e) {
      if (e.target !== d) return;
      var b = d.getBoundingClientRect();
      if (e.clientX < b.left || e.clientX > b.right || e.clientY < b.top || e.clientY > b.bottom) d.close();
    });
    d.addEventListener("close", clean);
    d.querySelectorAll("[data-dialog-close]").forEach(function (a) {
      a.addEventListener("click", function (e) { e.preventDefault(); d.close(); });
    });
  }
  document.querySelectorAll("a[data-dialog]").forEach(function (a) {
    var d = document.getElementById(a.dataset.dialog);
    if (!d || !d.showModal) return;
    wire(d);
    a.addEventListener("click", function (e) {
      e.preventDefault();
      if (!d.open) d.showModal();
    });
  });
  document.querySelectorAll("dialog[data-open]").forEach(function (d) {
    if (!d.showModal) return; // no support: it shows inline, open
    wire(d);
    d.close();
    d.showModal();
  });
})();
