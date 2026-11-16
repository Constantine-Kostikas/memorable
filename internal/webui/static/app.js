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

  var THEME_KEY = 'memorable.theme';
  var THEMES = ['system', 'light', 'dark'];

  function storedTheme() {
    var value = null;
    try {
      value = window.localStorage.getItem(THEME_KEY);
    } catch (e) {
      value = null;
    }
    return THEMES.indexOf(value) === -1 ? 'system' : value;
  }

  function storeTheme(theme) {
    try {
      if (theme === 'system') {
        window.localStorage.removeItem(THEME_KEY);
      } else {
        window.localStorage.setItem(THEME_KEY, theme);
      }
    } catch (e) {
      // Storage can be unavailable (private mode, blocked site data); the choice then lasts for this page only.
    }
  }

  function applyTheme(theme) {
    if (theme === 'system') {
      document.documentElement.removeAttribute('data-theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
    }
  }

  function currentTheme() {
    var value = document.documentElement.getAttribute('data-theme');
    return THEMES.indexOf(value) === -1 ? 'system' : value;
  }

  function labelToggle(button, theme) {
    var label = 'Theme: ' + theme;
    button.textContent = label;
    button.setAttribute('aria-label', label);
  }

  function bindThemeToggles(root) {
    var buttons = root.querySelectorAll ? root.querySelectorAll('[data-theme-toggle]') : [];
    Array.prototype.forEach.call(buttons, function (button) {
      labelToggle(button, currentTheme());
      if (button.hasAttribute('data-theme-bound')) {
        return;
      }
      button.setAttribute('data-theme-bound', '');
      button.addEventListener('click', function () {
        var next = THEMES[(THEMES.indexOf(currentTheme()) + 1) % THEMES.length];
        applyTheme(next);
        storeTheme(next);
        labelToggle(button, next);
      });
    });
  }

  function markSelectedRow(evt) {
    var detail = evt.detail || {};
    var link = detail.elt;
    if (!detail.successful || !link || !link.hasAttribute || !link.hasAttribute('data-row-link')) {
      return;
    }
    var current = document.querySelectorAll('[data-row-link][aria-current]');
    Array.prototype.forEach.call(current, function (row) {
      row.removeAttribute('aria-current');
    });
    link.setAttribute('aria-current', 'true');
  }

  applyTheme(storedTheme());
  onReady(bindThemeToggles);
  document.addEventListener('htmx:afterRequest', markSelectedRow);
})();
