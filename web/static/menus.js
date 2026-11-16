// Drop-down menus (<details class="menu">): the admin bar's ⚙ Settings and
// the People list's ⋯ menus. Without JS they still open and close with a tap
// on their button; this adds one-open-at-a-time, closing with a tap outside
// or Escape.
(function () {
  var menus = document.querySelectorAll("details.menu");
  document.addEventListener("click", function (e) {
    menus.forEach(function (m) { if (m.open && !m.contains(e.target)) m.open = false; });
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    menus.forEach(function (m) {
      if (!m.open) return;
      m.open = false;
      if (m.contains(document.activeElement)) m.querySelector("summary").focus();
    });
  });
  menus.forEach(function (m) {
    m.addEventListener("toggle", function () {
      if (m.open) menus.forEach(function (o) { if (o !== m) o.open = false; });
    });
  });
})();
