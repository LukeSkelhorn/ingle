// Ingle screensaver: a shuffled photo slideshow with a drifting info panel,
// shared by the board's screensaver and the locked display.
//
//   var saver = IngleSaver(el, { info: function (panel) { ... } });
//   saver.start(); saver.stop(); saver.showing();
//
// el holds .saver__photos and .saver__info. info(panel) is called on start
// and every minute to refresh the panel's contents.
(function () {
  "use strict";
  var SLIDE_MS = 20000;

  function shuffle(a) {
    for (var i = a.length - 1; i > 0; i--) {
      var j = Math.floor(Math.random() * (i + 1)), t = a[i];
      a[i] = a[j]; a[j] = t;
    }
    return a;
  }

  window.IngleSaver = function (saver, opts) {
    var layer = saver.querySelector(".saver__photos"),
        info = saver.querySelector(".saver__info"),
        showing = false, slideTimer, infoTimer,
        slides = [], at = 0, failures = 0, wakeLock = null;

    function refreshInfo() {
      // Drift the text a little each minute: kinder to an always-on panel.
      info.style.setProperty("--dx", Math.round(Math.random() * 40 - 20) + "px");
      info.style.setProperty("--dy", Math.round(Math.random() * 30) + "px");
      if (opts.info) opts.info(info);
    }

    function loadSlides() {
      fetch("/screensaver/photos", { credentials: "same-origin" })
        .then(function (r) { return r.ok ? r.json() : { photos: [] }; })
        .then(function (d) { slides = shuffle(d.photos || []); at = 0; failures = 0; next(); })
        .catch(function () { slides = []; next(); });
    }
    function next() {
      if (!showing) return;
      saver.classList.toggle("saver--empty", !slides.length);
      if (!slides.length || failures >= slides.length) return;
      if (at >= slides.length) { loadSlides(); return; } // new pass, new order, new photos
      var img = new Image();
      img.className = "saver__photo";
      img.alt = "";
      img.onload = function () {
        if (!showing) return;
        failures = 0;
        var old = layer.querySelectorAll(".saver__photo");
        layer.appendChild(img);
        void img.offsetWidth; // start the crossfade from transparent
        img.classList.add("is-shown");
        old.forEach(function (o) { o.classList.remove("is-shown"); setTimeout(function () { o.remove(); }, 2000); });
        slideTimer = setTimeout(next, SLIDE_MS);
      };
      img.onerror = function () { failures++; slideTimer = setTimeout(next, 500); };
      img.src = slides[at++];
    }

    function requestWakeLock() {
      if (!navigator.wakeLock || wakeLock) return;
      navigator.wakeLock.request("screen").then(function (l) {
        wakeLock = l;
        l.addEventListener("release", function () { wakeLock = null; });
      }).catch(function () {});
    }

    function start() {
      if (showing) return;
      showing = true;
      saver.hidden = false;
      void saver.offsetWidth;
      saver.classList.add("is-on");
      refreshInfo();
      infoTimer = setInterval(refreshInfo, 60000);
      loadSlides();
      requestWakeLock();
    }

    function stop() {
      if (!showing) return;
      showing = false;
      clearTimeout(slideTimer);
      clearInterval(infoTimer);
      saver.classList.remove("is-on");
      setTimeout(function () {
        if (showing) return;
        saver.hidden = true;
        layer.textContent = ""; // free the decoded photos
      }, 400);
      if (wakeLock) { wakeLock.release().catch(function () {}); wakeLock = null; }
    }

    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && showing) { requestWakeLock(); refreshInfo(); }
    });

    return {
      start: start,
      stop: stop,
      refresh: function () { if (showing) refreshInfo(); },
      showing: function () { return showing; },
    };
  };
})();
