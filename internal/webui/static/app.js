(function () {
  'use strict';

  function onReady(fn) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', function () { fn(document); });
    } else {
      fn(document);
    }
    document.addEventListener('htmx:load', function (evt) {
      fn((evt.detail && evt.detail.elt) || evt.target);
    });
  }
})();
