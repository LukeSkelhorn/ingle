(function () {
  function typingElsewhere(e) {
    var t = e.target;
    return t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));
  }

  var Pinpad = window.InglePinpad = function (root, opts) {
    var dots = root.querySelector(".pinpad__dots"),
        msg = root.querySelector(".pinpad__msg"),
        code = "", length = 0, shown = false;
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
        d.className = "pinpad__dot" + (i < code.length ? " is-filled" : "") + (shown ? " is-shown" : "");
        if (shown && i < code.length) d.textContent = code[i];
        dots.appendChild(d);
      }
    }
    function add(k) {
      if (code.length >= (length || 6)) return;
      code += k; msg.textContent = ""; draw();
      if (opts.onInput) opts.onInput();
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
      if (typingElsewhere(e) || e.metaKey || e.ctrlKey || e.altKey) return;
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
      show: function (on) { shown = on; draw(); },
    };
  };

  Pinpad.set = function (box) {
    var form = box.closest("form"),
        fields = box.querySelector(".pinset__fields"),
        padEl = box.querySelector("[data-pinpad]"),
        prompt = box.querySelector(".pinset__prompt"),
        showBtn = box.querySelector("[data-show]"),
        first = box.querySelector("[data-pin-first]"),
        again = box.querySelector("[data-pin-again]"),
        sends = box.dataset.submit === "1",
        entered = "", step = 1, ready = false, mine = false;

    // Hidden boxes can't be required: the browser would block the form silently.
    fields.hidden = true;
    padEl.hidden = false;
    first.required = false;
    if (again) again.required = false;

    document.addEventListener("pointerdown", function (e) { mine = padEl.contains(e.target); });
    document.addEventListener("focusin", function (e) { mine = padEl.contains(e.target); });

    function fill(v) {
      first.value = v;
      if (again) again.value = v;
    }
    function ask(n) {
      step = n;
      prompt.textContent = n === 1 ? box.dataset.label : box.dataset.again;
    }

    var pad = Pinpad(padEl, {
      active: function () { return mine; },
      onInput: function () {
        if (ready) { ready = false; fill(""); ask(1); }
      },
      onSubmit: function (code) {
        if (step === 1) {
          entered = code;
          ask(2);
          pad.reset("", code.length);
          return;
        }
        if (code !== entered) {
          ask(1);
          pad.reset("", 0);
          pad.error("Those didn't match. Start again.");
          return;
        }
        fill(code);
        if (sends) {
          if (form.requestSubmit) form.requestSubmit(); else form.submit();
          return;
        }
        ready = true;
        ask(1);
        pad.reset("", 0);
        prompt.textContent = "✓ PIN ready. Type a new one to change it.";
      },
    });
    ask(1);

    var dialog = box.closest("dialog");
    if (dialog) {
      dialog.addEventListener("close", function () {
        ready = false;
        fill("");
        ask(1);
        pad.reset("", 0);
      });
    }

    showBtn.addEventListener("click", function () {
      var on = showBtn.getAttribute("aria-pressed") !== "true";
      showBtn.setAttribute("aria-pressed", on ? "true" : "false");
      showBtn.textContent = on ? "Hide digits" : "Show digits";
      pad.show(on);
    });

    form.addEventListener("submit", function (e) {
      if (first.value) return;
      e.preventDefault();
      pad.error(step === 2 ? "Type it again to confirm." : "Type the PIN on the keypad.");
      padEl.scrollIntoView({ block: "center" });
      padEl.focus();
      mine = true;
    }, true);
  };

  document.querySelectorAll("[data-pinpad-set]").forEach(Pinpad.set);
})();
