// DomusPinpad drives the shared PIN pad (the "pinpad" template): digits
// from the keys or a keyboard, drawn as dots. When the PIN's length is known
// (data-length, or reset's second argument) it shows that many dots and
// submits as soon as the last digit goes in. Otherwise it takes up to six,
// and → or Enter submits once there are at least four.
// opts.onSubmit(code) does the unlock; opts.active() (optional) says whether
// keyboard input is for this pad.
(function () {
  window.DomusPinpad = function (root, opts) {
    var dots = root.querySelector(".pinpad__dots"),
        msg = root.querySelector(".pinpad__msg"),
        code = "", length = 0;
    opts = opts || {};

    function setLength(n) {
      n = parseInt(n, 10);
      length = n >= 4 && n <= 6 ? n : 0;
    }
    setLength(root.dataset.length);

    function draw() {
      dots.textContent = "";
      for (var i = 0; i < Math.max(length || 4, code.length); i++) {
        var d = document.createElement("span");
        d.className = "pinpad__dot" + (i < code.length ? " is-filled" : "");
        dots.appendChild(d);
      }
    }
    function add(k) {
      if (code.length >= (length || 6)) return;
      code += k; msg.textContent = ""; draw();
      if (length && code.length === length) submit();
    }
    function back() { code = code.slice(0, -1); draw(); }
    function submit() { if (code.length >= 4 && opts.onSubmit) opts.onSubmit(code); }

    root.addEventListener("click", function (e) {
      var k = e.target.closest("[data-key]");
      if (k) add(k.dataset.key);
      else if (e.target.closest("[data-back]")) back();
      else if (e.target.closest("[data-go]")) submit();
    });
    document.addEventListener("keydown", function (e) {
      if (opts.active && !opts.active()) return;
      if (/^[0-9]$/.test(e.key)) add(e.key);
      else if (e.key === "Backspace") back();
      else if (e.key === "Enter") { e.preventDefault(); submit(); }
    });
    draw();

    return {
      reset: function (text, n) {
        if (n !== undefined) setLength(n);
        code = ""; msg.textContent = text || ""; draw();
      },
      error: function (text) {
        code = ""; msg.textContent = text; draw();
        root.classList.remove("is-wrong"); void root.offsetWidth; root.classList.add("is-wrong");
      },
    };
  };
})();
