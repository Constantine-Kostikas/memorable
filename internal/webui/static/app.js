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

  function each(root, selector, fn) {
    if (!root || !root.querySelectorAll) {
      return;
    }
    if (root.matches && root.matches(selector)) {
      fn(root);
    }
    Array.prototype.forEach.call(root.querySelectorAll(selector), fn);
  }

  function isTextEntry(el) {
    if (!el || !el.tagName) {
      return false;
    }
    var tag = el.tagName.toLowerCase();
    return tag === 'input' || tag === 'textarea' || tag === 'select' || el.isContentEditable;
  }

  // Editor: dirty tracking against the stored values the server renders in data-original.

  function normalizeText(value) {
    return String(value == null ? '' : value).replace(/\r\n?/g, '\n');
  }

  function fieldChanged(field) {
    var original = normalizeText(field.getAttribute('data-original'));
    if (field.type === 'checkbox') {
      return field.checked !== (original === 'on');
    }
    if (field.tagName.toLowerCase() === 'input') {
      // Browsers strip line breaks from a text input's value, so a stored title with one would never match.
      original = original.replace(/\n/g, '');
    }
    return normalizeText(field.value) !== original;
  }

  function isDirty(form) {
    if (form.hasAttribute('data-unsaved')) {
      return true;
    }
    return Array.prototype.some.call(form.querySelectorAll('[data-original]'), fieldChanged);
  }

  function currentEditor() {
    return document.querySelector('#detail-pane [data-editor]');
  }

  function editorIsDirty() {
    var form = currentEditor();
    return !!form && isDirty(form);
  }

  function editorTitle(form) {
    return (form && form.getAttribute('data-title')) || 'this memory';
  }

  function renderDirtyState(form) {
    var dirty = isDirty(form);
    var pane = form.closest('#detail-pane') || document;
    each(form, '[data-dirty-marker]', function (marker) {
      marker.hidden = !dirty;
    });
    each(pane, '[data-save]', function (save) {
      save.disabled = !dirty;
    });
    each(pane, '[data-discard]', function (discard) {
      if (dirty) {
        discard.removeAttribute('aria-disabled');
      } else {
        discard.setAttribute('aria-disabled', 'true');
      }
    });
  }

  function bindEditors(root) {
    each(root, '[data-editor]', renderDirtyState);
  }

  function onEditorInput(evt) {
    var form = evt.target && evt.target.form;
    if (form && form.hasAttribute('data-editor')) {
      renderDirtyState(form);
    }
  }

  function ignoreCleanDiscard(evt) {
    var discard = evt.target && evt.target.closest && evt.target.closest('[data-discard][aria-disabled="true"]');
    if (discard) {
      evt.preventDefault();
      evt.stopPropagation();
    }
  }

  // Ctrl+S / Cmd+S: never open the browser's save dialog from the editor; submit only when dirty.

  function focusedEditor() {
    var el = document.activeElement;
    if (!el || el === document.body) {
      return null;
    }
    var form = el.closest ? el.closest('[data-editor]') : null;
    if (!form && el.form && el.form.hasAttribute('data-editor')) {
      form = el.form;
    }
    return form;
  }

  function saveShortcut(evt) {
    var isSaveKey = (evt.ctrlKey || evt.metaKey) && !evt.altKey && !evt.shiftKey && String(evt.key).toLowerCase() === 's';
    if (!isSaveKey) {
      return;
    }
    var form = focusedEditor();
    if (!form) {
      return;
    }
    evt.preventDefault();
    if (isDirty(form) && form.hasAttribute('hx-post')) {
      form.requestSubmit();
    }
  }

  // Unsaved-changes guard for htmx requests that would replace the pane, and for full page loads.

  function replacesPane(target) {
    var pane = document.getElementById('detail-pane');
    return !!pane && !!target && (target === pane || (target.contains && target.contains(pane)));
  }

  function guardPaneReplacement(evt) {
    var detail = evt.detail || {};
    var form = currentEditor();
    if (!form || detail.elt === form || !replacesPane(detail.target) || !isDirty(form)) {
      return;
    }
    evt.preventDefault();
    if (confirmDiscard(form)) {
      // Skip htmx's own hx-confirm prompt only when the element has none, so Delete still asks its question.
      detail.issueRequest(!detail.question);
    }
  }

  function confirmDiscard(form) {
    return window.confirm('Discard unsaved changes to ' + editorTitle(form) + '?');
  }

  var lastUrl = window.location.href;

  function rememberUrl() {
    lastUrl = window.location.href;
  }

  // Runs before htmx's window.onpopstate (assigned later, during htmx's deferred init), which would swap the body.
  function guardHistory(evt) {
    var form = currentEditor();
    if (!evt.state || !evt.state.htmx || !form || !isDirty(form)) {
      return;
    }
    if (confirmDiscard(form)) {
      return;
    }
    evt.stopImmediatePropagation();
    window.history.pushState({ htmx: true }, '', lastUrl);
  }

  function guardUnload(evt) {
    if (editorIsDirty()) {
      evt.preventDefault();
      evt.returnValue = '';
    }
  }

  // Tabs: mark the tab for the section in the URL hash, defaulting to Content.

  var TAB_SECTIONS = ['content', 'versions', 'links', 'activity'];

  function selectTab(root, section) {
    var active = TAB_SECTIONS.indexOf(section) === -1 ? 'content' : section;
    each(root, '[role="tab"]', function (tab) {
      tab.setAttribute('aria-selected', tab.getAttribute('aria-controls') === active ? 'true' : 'false');
    });
  }

  function hashSection() {
    return window.location.hash.replace(/^#/, '');
  }

  function bindTabs(root) {
    each(root, '[role="tablist"]', function (list) {
      selectTab(list, hashSection());
    });
  }

  function onTabClick(evt) {
    var tab = evt.target && evt.target.closest && evt.target.closest('[role="tab"]');
    if (tab) {
      selectTab(tab.closest('[role="tablist"]') || document, tab.getAttribute('aria-controls'));
    }
  }

  function onHashChange() {
    rememberUrl();
    bindTabs(document);
  }

  // Toasts: dismiss on click; notices leave after five seconds, errors stay until dismissed.

  var TOAST_LIFETIME_MS = 5000;

  function scheduleToast(toast) {
    if (toast.hasAttribute('data-toast-bound')) {
      return;
    }
    toast.setAttribute('data-toast-bound', '');
    if (toast.getAttribute('role') === 'alert') {
      return;
    }
    window.setTimeout(function () {
      toast.remove();
    }, TOAST_LIFETIME_MS);
  }

  function bindToasts(root) {
    each(root, '[data-toast]', scheduleToast);
  }

  function onToastDismiss(evt) {
    var button = evt.target && evt.target.closest && evt.target.closest('[data-toast-dismiss]');
    var toast = button && button.closest('[data-toast]');
    if (toast) {
      toast.remove();
    }
  }

  // Expand: the editor takes the whole window; the choice persists across selections and reloads.

  var EXPANDED_KEY = 'memorable.expanded';

  function storedExpanded() {
    try {
      return window.localStorage.getItem(EXPANDED_KEY) === 'true';
    } catch (e) {
      return false;
    }
  }

  function storeExpanded(expanded) {
    try {
      if (expanded) {
        window.localStorage.setItem(EXPANDED_KEY, 'true');
      } else {
        window.localStorage.removeItem(EXPANDED_KEY);
      }
    } catch (e) {
      // Storage can be unavailable; the choice then lasts for this page only.
    }
  }

  function isExpanded() {
    return !!document.body && document.body.getAttribute('data-expanded') === 'true';
  }

  function renderExpandToggles(root) {
    var expanded = isExpanded();
    each(root, '[data-expand-toggle]', function (button) {
      button.hidden = false;
      button.setAttribute('aria-pressed', expanded ? 'true' : 'false');
    });
  }

  function setExpanded(expanded) {
    if (expanded) {
      document.body.setAttribute('data-expanded', 'true');
    } else {
      document.body.removeAttribute('data-expanded');
    }
    storeExpanded(expanded);
    renderExpandToggles(document);
  }

  function bindExpand(root) {
    if (root === document && storedExpanded()) {
      document.body.setAttribute('data-expanded', 'true');
    }
    renderExpandToggles(root);
  }

  function onExpandClick(evt) {
    if (evt.target && evt.target.closest && evt.target.closest('[data-expand-toggle]')) {
      setExpanded(!isExpanded());
    }
  }

  function expandShortcut(evt) {
    if (evt.defaultPrevented || evt.ctrlKey || evt.metaKey || evt.altKey || evt.isComposing) {
      return;
    }
    if (evt.key === 'Escape') {
      if (isExpanded() && document.querySelector('[data-expand-toggle]')) {
        setExpanded(false);
      }
      return;
    }
    if ((evt.key === 'f' || evt.key === 'F') && !evt.repeat && !isTextEntry(evt.target) &&
        document.querySelector('[data-expand-toggle]')) {
      evt.preventDefault();
      setExpanded(!isExpanded());
    }
  }

  applyTheme(storedTheme());
  onReady(bindThemeToggles);
  onReady(bindExpand);
  onReady(bindEditors);
  onReady(bindTabs);
  onReady(bindToasts);
  document.addEventListener('htmx:afterRequest', markSelectedRow);
  document.addEventListener('htmx:confirm', guardPaneReplacement);
  document.addEventListener('input', onEditorInput);
  document.addEventListener('change', onEditorInput);
  document.addEventListener('click', ignoreCleanDiscard, true);
  document.addEventListener('click', onExpandClick);
  document.addEventListener('click', onTabClick);
  document.addEventListener('click', onToastDismiss);
  document.addEventListener('keydown', saveShortcut);
  document.addEventListener('keydown', expandShortcut);
  document.addEventListener('htmx:pushedIntoHistory', rememberUrl);
  document.addEventListener('htmx:replacedInHistory', rememberUrl);
  window.addEventListener('popstate', guardHistory);
  window.addEventListener('beforeunload', guardUnload);
  window.addEventListener('hashchange', onHashChange);
})();
