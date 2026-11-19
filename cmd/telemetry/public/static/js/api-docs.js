(function () {
  'use strict';
  window.RunUI.initChrome();
  // The examples name the address this page was opened on.
  document.querySelectorAll('[data-origin]').forEach(function (el) { el.textContent = location.origin; });
})();
