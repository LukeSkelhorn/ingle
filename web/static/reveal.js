// An eye at the end of each password box shows or hides what's typed. PIN
// boxes are left alone.
(function () {
  var eye = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg>';
  var eyeOff = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10.6 5.1A10.7 10.7 0 0 1 12 5c6.4 0 10 7 10 7a17.6 17.6 0 0 1-2.9 3.8M6.6 6.6C3.8 8.4 2 12 2 12s3.6 7 10 7a9.8 9.8 0 0 0 5.4-1.6"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/><path d="M2 2l20 20"/></svg>';
  document.querySelectorAll('input[type="password"]:not([inputmode="numeric"])').forEach(function (input) {
    var wrap = document.createElement("div");
    wrap.className = "reveal";
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);
    var btn = document.createElement("button");
    btn.type = "button";
    btn.className = "reveal__btn";
    function show(on) {
      input.type = on ? "text" : "password";
      btn.innerHTML = on ? eyeOff : eye;
      btn.setAttribute("aria-label", on ? "Hide password" : "Show password");
      btn.setAttribute("aria-pressed", on ? "true" : "false");
    }
    btn.addEventListener("click", function () { show(input.type === "password"); input.focus(); });
    // Hide it again before the form is sent, so the browser saves it as a password.
    if (input.form) input.form.addEventListener("submit", function () { show(false); });
    show(false);
    wrap.appendChild(btn);
  });
})();
