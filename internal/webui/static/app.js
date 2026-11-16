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
    if (field.tagName.toLowerCase() === 'input' && field.type !== 'hidden') {
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

  // Expiry: the "Never" button clears the date input; the input event lets dirty tracking see it.

  function bindExpiryClear(root) {
    each(root, '[data-clear-expiry]', function (button) {
      button.hidden = false;
    });
  }

  function onExpiryClear(evt) {
    var button = evt.target && evt.target.closest && evt.target.closest('[data-clear-expiry]');
    var input = button && document.getElementById(button.getAttribute('aria-controls'));
    if (!input || input.disabled) {
      return;
    }
    if (input.value !== '') {
      input.value = '';
      input.dispatchEvent(new Event('input', { bubbles: true }));
    }
    input.focus();
  }

  // Tags: chips over the comma-separated tags field. The field stays the submitted value and the dirty
  // baseline; chips are rebuilt from it on every bind, so restored history snapshots stay consistent.

  function splitTagText(value) {
    return String(value == null ? '' : value).split(',').map(function (tag) {
      return tag.trim();
    }).filter(function (tag) {
      return tag !== '';
    });
  }

  function uniqueTags(tags) {
    return tags.filter(function (tag, index) {
      return tags.indexOf(tag) === index;
    });
  }

  function tagParts(widget) {
    return {
      source: widget.querySelector('[data-tags-source]'),
      chips: widget.querySelector('[data-tag-chips]'),
      list: widget.querySelector('[data-tag-list]'),
      entry: widget.querySelector('[data-tag-entry]'),
      template: widget.querySelector('template[data-tag-chip]')
    };
  }

  function chipTags(parts) {
    return Array.prototype.map.call(parts.list.querySelectorAll('[data-tag-text]'), function (text) {
      return text.textContent;
    });
  }

  function appendChip(parts, tag) {
    var chip = parts.template.content.firstElementChild.cloneNode(true);
    chip.querySelector('[data-tag-text]').textContent = tag;
    chip.querySelector('[data-tag-remove]').setAttribute('aria-label', 'Remove tag ' + tag);
    parts.list.appendChild(chip);
  }

  // Writes the chips plus any pending entry text back to the field, joined as the server renders it.
  function syncTags(parts) {
    var value = uniqueTags(chipTags(parts).concat(splitTagText(parts.entry.value))).join(', ');
    if (parts.source.value !== value) {
      parts.source.value = value;
      parts.source.dispatchEvent(new Event('input', { bubbles: true }));
    }
  }

  function commitTagEntry(parts) {
    var existing = chipTags(parts);
    splitTagText(parts.entry.value).forEach(function (tag) {
      if (existing.indexOf(tag) === -1) {
        appendChip(parts, tag);
        existing.push(tag);
      }
    });
    parts.entry.value = '';
    syncTags(parts);
  }

  function renderTagChips(widget) {
    var parts = tagParts(widget);
    if (!parts.source || !parts.chips || !parts.list || !parts.entry || !parts.template || parts.source.disabled) {
      return;
    }
    parts.list.textContent = '';
    parts.entry.value = '';
    uniqueTags(splitTagText(parts.source.value)).forEach(function (tag) {
      appendChip(parts, tag);
    });
    parts.source.hidden = true;
    parts.chips.hidden = false;
    each(widget, '[data-tags-hint]', function (hint) {
      hint.hidden = true;
    });
    each(widget, '[data-tags-label]', function (label) {
      label.htmlFor = parts.entry.id;
    });
  }

  function bindTagChips(root) {
    each(root, '[data-tags]', renderTagChips);
  }

  function tagEntryParts(target) {
    var widget = target && target.matches && target.matches('[data-tag-entry]') && target.closest('[data-tags]');
    return widget ? tagParts(widget) : null;
  }

  function onTagKeydown(evt) {
    var parts = tagEntryParts(evt.target);
    if (!parts || evt.isComposing) {
      return;
    }
    if (evt.key === 'Enter' || evt.key === ',') {
      evt.preventDefault();
      commitTagEntry(parts);
      return;
    }
    if (evt.key === 'Backspace' && parts.entry.value === '' && parts.list.lastElementChild) {
      evt.preventDefault();
      parts.list.lastElementChild.remove();
      syncTags(parts);
    }
  }

  function onTagInput(evt) {
    var parts = tagEntryParts(evt.target);
    if (!parts) {
      return;
    }
    if (parts.entry.value.indexOf(',') !== -1) {
      commitTagEntry(parts);
    } else {
      syncTags(parts);
    }
  }

  function onTagBlur(evt) {
    var parts = tagEntryParts(evt.target);
    if (parts && parts.entry.value.trim() !== '') {
      commitTagEntry(parts);
    }
  }

  function onTagRemove(evt) {
    var button = evt.target && evt.target.closest && evt.target.closest('[data-tag-remove]');
    var widget = button && button.closest('[data-tags]');
    if (!widget) {
      return;
    }
    var parts = tagParts(widget);
    button.closest('li').remove();
    syncTags(parts);
    parts.entry.focus();
  }

  // Lookup pickers: the visible input (ref_display) searches /lookup; picking an option fills the hidden
  // field the form submits. Without JS the visible input keeps its real name and submits the typed id or slug.

  var LOOKUP_DISPLAY = 'ref_display';

  function lookupField(input) {
    var name = input.getAttribute('data-lookup');
    var form = input.form;
    if (!name || !form) {
      return null;
    }
    return Array.prototype.find.call(form.elements, function (el) {
      return el.type === 'hidden' && el.name === name;
    }) || null;
  }

  function bindLookups(root) {
    each(root, '[data-lookup]', function (input) {
      var name = input.getAttribute('data-lookup');
      if (!name || !input.form) {
        return;
      }
      input.name = LOOKUP_DISPLAY;
      if (!lookupField(input)) {
        var hidden = document.createElement('input');
        hidden.type = 'hidden';
        hidden.name = name;
        input.form.appendChild(hidden);
      }
    });
  }

  function lookupList(input) {
    return document.getElementById(input.getAttribute('aria-controls'));
  }

  function lookupOptions(list) {
    return list ? list.querySelectorAll('[role="option"]') : [];
  }

  function lookupInputFor(list) {
    return list && list.id ? document.querySelector('[data-lookup][aria-controls="' + list.id + '"]') : null;
  }

  function activeOptionIndex(list) {
    return Array.prototype.findIndex.call(lookupOptions(list), function (option) {
      return option.getAttribute('aria-selected') === 'true';
    });
  }

  function setActiveOption(input, index) {
    var list = lookupList(input);
    input.removeAttribute('aria-activedescendant');
    Array.prototype.forEach.call(lookupOptions(list), function (option, i) {
      var active = i === index;
      option.setAttribute('aria-selected', active ? 'true' : 'false');
      if (active) {
        option.id = option.id || list.id + '-' + i;
        input.setAttribute('aria-activedescendant', option.id);
        option.scrollIntoView({ block: 'nearest' });
      }
    });
  }

  function isLookupOpen(input) {
    var list = lookupList(input);
    return !!list && !list.hidden;
  }

  function showLookup(input, open) {
    var list = lookupList(input);
    if (!list) {
      return;
    }
    var visible = open && list.children.length > 0;
    list.hidden = !visible;
    input.setAttribute('aria-expanded', visible ? 'true' : 'false');
    if (!visible) {
      setActiveOption(input, -1);
    }
  }

  function pickOption(input, option) {
    var field = lookupField(input);
    var title = option.querySelector('[data-option-title]');
    if (field) {
      field.value = option.getAttribute('data-id') || '';
    }
    input.value = title ? title.textContent : option.getAttribute('data-id') || '';
    showLookup(input, false);
  }

  function isLookupInput(el) {
    return !!el && !!el.matches && el.matches('[data-lookup]');
  }

  function onLookupConfigRequest(evt) {
    var detail = evt.detail || {};
    if (isLookupInput(detail.elt)) {
      detail.parameters.q = detail.elt.value;
      delete detail.parameters[LOOKUP_DISPLAY];
    }
  }

  function onLookupSwap(evt) {
    var input = lookupInputFor(evt.detail && evt.detail.target);
    if (!input) {
      return;
    }
    var field = lookupField(input);
    setActiveOption(input, -1);
    showLookup(input, document.activeElement === input && (!field || field.value === ''));
  }

  function onLookupInput(evt) {
    if (!isLookupInput(evt.target)) {
      return;
    }
    // Any edit after a pick drops it, so the form never submits an id the input no longer shows.
    var field = lookupField(evt.target);
    if (field) {
      field.value = '';
    }
    if (evt.target.value.trim() === '') {
      showLookup(evt.target, false);
    }
  }

  function onLookupKeydown(evt) {
    var input = evt.target;
    if (!isLookupInput(input) || evt.isComposing) {
      return;
    }
    var options = lookupOptions(lookupList(input));
    var index = activeOptionIndex(lookupList(input));
    switch (evt.key) {
      case 'ArrowDown':
      case 'ArrowUp':
        if (options.length === 0) {
          return;
        }
        evt.preventDefault();
        showLookup(input, true);
        if (evt.key === 'ArrowDown') {
          setActiveOption(input, index < options.length - 1 ? index + 1 : 0);
        } else {
          setActiveOption(input, index > 0 ? index - 1 : options.length - 1);
        }
        return;
      case 'Enter':
        if (isLookupOpen(input) && index !== -1) {
          evt.preventDefault();
          pickOption(input, options[index]);
        }
        return;
      case 'Escape':
        if (isLookupOpen(input)) {
          evt.preventDefault();
          showLookup(input, false);
        }
        return;
      case 'Tab':
        showLookup(input, false);
        return;
    }
  }

  function onLookupMousedown(evt) {
    var option = evt.target && evt.target.closest && evt.target.closest('[role="listbox"] [role="option"]');
    var input = option && lookupInputFor(option.closest('[role="listbox"]'));
    if (input) {
      // Keep focus in the input so its blur does not close the list before the pick lands.
      evt.preventDefault();
      pickOption(input, option);
    }
  }

  function onLookupBlur(evt) {
    if (isLookupInput(evt.target)) {
      showLookup(evt.target, false);
    }
  }

  // Runs in the capture phase, before htmx's submit handler on the form reads its values.
  function onLookupSubmit(evt) {
    each(evt.target, '[data-lookup]', function (input) {
      var field = lookupField(input);
      if (field && field.value === '') {
        field.value = input.value.trim();
      }
    });
  }

  applyTheme(storedTheme());
  onReady(bindThemeToggles);
  onReady(bindExpand);
  onReady(bindEditors);
  onReady(bindTabs);
  onReady(bindToasts);
  onReady(bindExpiryClear);
  onReady(bindTagChips);
  onReady(bindLookups);
  document.addEventListener('htmx:afterRequest', markSelectedRow);
  document.addEventListener('htmx:configRequest', onLookupConfigRequest);
  document.addEventListener('htmx:afterSwap', onLookupSwap);
  // Capture phase: these run before the document-level shortcuts below, which skip prevented events.
  document.addEventListener('keydown', onTagKeydown, true);
  document.addEventListener('keydown', onLookupKeydown, true);
  document.addEventListener('submit', onLookupSubmit, true);
  document.addEventListener('input', onTagInput);
  document.addEventListener('input', onLookupInput);
  document.addEventListener('focusout', onTagBlur);
  document.addEventListener('focusout', onLookupBlur);
  document.addEventListener('mousedown', onLookupMousedown);
  document.addEventListener('click', onExpiryClear);
  document.addEventListener('click', onTagRemove);
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
  // A cancelled popstate stops at guardHistory; every other one lands here, so lastUrl tracks restores too.
  window.addEventListener('popstate', rememberUrl);
  window.addEventListener('beforeunload', guardUnload);
  window.addEventListener('hashchange', onHashChange);
})();
