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
    if (isDirty(form) && form.hasAttribute('hx-post') && expiryReady(form)) {
      form.requestSubmit();
    }
  }

  // Unsaved-changes guard for htmx requests that would replace the pane, and for full page loads.

  function replacesPane(target) {
    var pane = document.getElementById('detail-pane');
    return !!pane && !!target && (target === pane || (target.contains && target.contains(pane)));
  }

  // Set when the user keeps their edits; confirm is synchronous, so a caller can read it right after a click.
  var paneReplacementDeclined = false;

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
    } else {
      paneReplacementDeclined = true;
    }
  }

  function confirmDiscard(form) {
    return window.confirm('Discard unsaved changes to ' + editorTitle(form) + '?');
  }

  var lastUrl = window.location.href;

  function rememberUrl() {
    lastUrl = window.location.href;
  }

  function sameDocument(a, b) {
    return a.pathname === b.pathname && a.search === b.search;
  }

  function onlyHashChanged() {
    try {
      return sameDocument(new URL(lastUrl, window.location.href), window.location);
    } catch (e) {
      return false;
    }
  }

  // Runs before htmx's window.onpopstate (assigned later, during htmx's deferred init), which would swap the body.
  function guardHistory(evt) {
    if (onlyHashChanged()) {
      // Back or forward between in-pane anchors (#versions) is not a page change: keep htmx from re-fetching.
      // This also skips rememberUrl below; hashchange fires next and updates lastUrl and the section nav.
      evt.stopImmediatePropagation();
      return;
    }
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

  // Section nav: mark the link of the section in the URL hash as current, defaulting to Content.

  var SECTIONS = ['content', 'versions', 'links', 'activity'];

  function selectSection(nav, section) {
    var active = '#' + (SECTIONS.indexOf(section) === -1 ? 'content' : section);
    each(nav, 'a[href^="#"]', function (link) {
      if (link.getAttribute('href') === active) {
        link.setAttribute('aria-current', 'true');
      } else {
        link.removeAttribute('aria-current');
      }
    });
  }

  function hashSection() {
    return window.location.hash.replace(/^#/, '');
  }

  function bindSectionNav(root) {
    each(root, '[data-section-nav]', function (nav) {
      selectSection(nav, hashSection());
    });
  }

  function onSectionClick(evt) {
    var link = evt.target && evt.target.closest && evt.target.closest('[data-section-nav] a[href^="#"]');
    if (link) {
      var section = link.getAttribute('href').replace(/^#/, '');
      selectSection(link.closest('[data-section-nav]'), section);
      openSectionFold(section);
    }
  }

  function onHashChange() {
    rememberUrl();
    bindSectionNav(document);
    openSectionFold(hashSection());
  }

  // Section folds: a heading button collapses its section. Only heading clicks change the stored collapsed set;
  // opening a section for a link, a validation error, find-in-page, or the expanded editor is not a preference.

  var COLLAPSED_KEY = 'memorable.collapsed';

  function storedCollapsed() {
    try {
      var value = JSON.parse(window.localStorage.getItem(COLLAPSED_KEY) || '[]');
      return Array.isArray(value) ? value : [];
    } catch (e) {
      return [];
    }
  }

  function storeCollapsed(name, collapsed) {
    var names = storedCollapsed().filter(function (other) { return other !== name; });
    if (collapsed) {
      names.push(name);
    }
    try {
      if (names.length) {
        window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify(names));
      } else {
        window.localStorage.removeItem(COLLAPSED_KEY);
      }
    } catch (e) {
      // Storage can be unavailable; folds then reset on the next selection.
    }
  }

  function setFoldOpen(fold, open) {
    var button = fold && fold.querySelector('[data-section-toggle]');
    var body = fold && fold.querySelector('[data-section-body]');
    if (!button || !body) {
      return;
    }
    button.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (open) {
      body.removeAttribute('hidden');
    } else {
      body.setAttribute('hidden', 'until-found');
    }
  }

  function isFoldOpen(fold) {
    var body = fold.querySelector('[data-section-body]');
    return !!body && !body.hasAttribute('hidden');
  }

  function openSectionFold(section) {
    if (SECTIONS.indexOf(section) !== -1) {
      setFoldOpen(document.querySelector('[data-section-fold="' + section + '"]'), true);
    }
  }

  function isForcedOpen(name) {
    return name === hashSection() || (name === 'content' && isExpanded());
  }

  function bindSectionFolds(root) {
    var collapsed = storedCollapsed();
    each(root, '[data-section-fold]', function (fold) {
      var name = fold.getAttribute('data-section-fold');
      if (collapsed.indexOf(name) !== -1 && !isForcedOpen(name)) {
        setFoldOpen(fold, false);
      }
    });
  }

  function onSectionToggle(evt) {
    var button = evt.target && evt.target.closest && evt.target.closest('[data-section-toggle]');
    var fold = button && button.closest('[data-section-fold]');
    if (!fold) {
      return;
    }
    var name = fold.getAttribute('data-section-fold');
    if (name === 'content' && isExpanded()) {
      return;
    }
    var open = !isFoldOpen(fold);
    setFoldOpen(fold, open);
    storeCollapsed(name, !open);
  }

  // invalid and beforematch do not bubble; the capture phase still sees them.
  function openEnclosingFold(evt) {
    var fold = evt.target && evt.target.closest && evt.target.closest('[data-section-fold]');
    if (fold && !isFoldOpen(fold)) {
      setFoldOpen(fold, true);
    }
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
      openSectionFold('content');
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
    if (evt.defaultPrevented || evt.ctrlKey || evt.metaKey || evt.altKey || evt.isComposing || openDialog()) {
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
    // A half-typed date already reads as "", but clearing still wipes the typed parts and its invalid mark.
    if (input.value !== '' || (input.validity && input.validity.badInput)) {
      input.value = '';
      input.dispatchEvent(new Event('input', { bubbles: true }));
    }
    input.focus();
  }

  // A half-typed date reads as "" and would clear the expiry on save, so saving waits until it is finished or cleared.

  var UNFINISHED_EXPIRY = 'Finish the expiry date or press Never.';

  function showErrorToast(message) {
    var region = document.getElementById('toasts');
    if (!region) {
      return;
    }
    // Errors stay until dismissed, so a repeated Ctrl+S must not stack copies of the same one.
    var already = Array.prototype.some.call(region.querySelectorAll('[data-toast] > span'), function (span) {
      return span.textContent === message;
    });
    if (already) {
      return;
    }
    var toast = document.createElement('div');
    toast.setAttribute('role', 'alert');
    toast.className = 'toast notice-danger';
    toast.setAttribute('data-toast', '');
    var text = document.createElement('span');
    text.textContent = message;
    var dismiss = document.createElement('button');
    dismiss.type = 'button';
    dismiss.className = 'toast-dismiss';
    dismiss.setAttribute('data-toast-dismiss', '');
    dismiss.setAttribute('aria-label', 'Dismiss error');
    dismiss.textContent = '\u00d7';
    toast.appendChild(text);
    toast.appendChild(dismiss);
    region.appendChild(toast);
    scheduleToast(toast);
  }

  // Reports whether form may be saved; an unfinished expiry date is marked, focused, and explained instead.
  function expiryReady(form) {
    var input = form && form.elements ? form.elements.namedItem('expires_at') : null;
    if (!input || !input.validity || !input.validity.badInput) {
      return true;
    }
    input.setAttribute('aria-invalid', 'true');
    input.focus();
    showErrorToast(UNFINISHED_EXPIRY);
    return false;
  }

  function clearExpiryInvalid(evt) {
    var input = evt.target;
    if (input && input.name === 'expires_at' && input.hasAttribute && input.hasAttribute('aria-invalid')) {
      input.removeAttribute('aria-invalid');
    }
  }

  // Capture phase: runs before the browser validates the form and before htmx sees the submit.
  function guardSaveClick(evt) {
    var save = evt.target && evt.target.closest && evt.target.closest('[data-save]');
    if (save && !expiryReady(save.form)) {
      evt.preventDefault();
      evt.stopPropagation();
    }
  }

  // Enter in a field submits without the Save button; stop it here before htmx's submit handler.
  function guardEditorSubmit(evt) {
    var form = evt.target;
    if (form && form.hasAttribute && form.hasAttribute('data-editor') && !expiryReady(form)) {
      evt.preventDefault();
      evt.stopPropagation();
    }
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


  // Dialogs: Esc closes the open one and focus returns to whatever opened it.

  var dialogOpener = null;

  function openDialog() {
    return document.querySelector('dialog[open]');
  }

  function showDialog(dialog) {
    if (!dialog || dialog.open || typeof dialog.showModal !== 'function') {
      return false;
    }
    cancelPendingRow();
    var active = document.activeElement;
    dialogOpener = active && active !== document.body ? active : null;
    dialog.showModal();
    return true;
  }

  function restoreFocus() {
    var opener = dialogOpener;
    dialogOpener = null;
    if (opener && opener.isConnected && typeof opener.focus === 'function') {
      opener.focus();
    }
  }

  // Link peeks: Esc hides an open peek card until the pointer or focus leaves its link.
  function openLinkPeek() {
    var peeks = document.querySelectorAll('[data-link-peek]:hover, [data-link-peek]:focus-within');
    return Array.prototype.find.call(peeks, function (peek) {
      var card = peek.querySelector('[role="tooltip"]');
      return !peek.hasAttribute('data-dismissed') && card && window.getComputedStyle(card).visibility === 'visible';
    });
  }

  // Capture phase, so the Esc that hides a peek does not also leave Expand.
  function onLinkPeekEscape(evt) {
    if (evt.key !== 'Escape' || evt.isComposing || openDialog()) {
      return;
    }
    var peek = openLinkPeek();
    if (peek) {
      evt.preventDefault();
      peek.setAttribute('data-dismissed', '');
    }
  }

  function onLinkPeekLeave(evt) {
    var peek = evt.target && evt.target.closest && evt.target.closest('[data-link-peek][data-dismissed]');
    if (peek && !(evt.relatedTarget && peek.contains(evt.relatedTarget))) {
      peek.removeAttribute('data-dismissed');
    }
  }

  // Capture phase: the dialog closes itself on Esc, so the Esc that leaves Expand never sees it.
  function onDialogEscape(evt) {
    var dialog = openDialog();
    if (!dialog || evt.key !== 'Escape' || evt.isComposing) {
      return;
    }
    evt.preventDefault();
    dialog.close();
  }

  function onDialogClose(evt) {
    if (evt.target && evt.target.tagName && evt.target.tagName.toLowerCase() === 'dialog') {
      restoreFocus();
    }
  }

  function onDialogCloseClick(evt) {
    var button = evt.target && evt.target.closest && evt.target.closest('[data-dialog-close]');
    var dialog = button && button.closest('dialog');
    if (dialog && dialog.open) {
      dialog.close();
    }
  }

  // Command palette: static page entries while the query is empty, /lookup?for=jump results otherwise.

  function paletteParts() {
    var dialog = document.getElementById('palette');
    if (!dialog) {
      return null;
    }
    return {
      dialog: dialog,
      input: dialog.querySelector('[data-palette-input]'),
      pages: document.getElementById('palette-pages'),
      results: document.getElementById('palette-results')
    };
  }

  function paletteList(parts) {
    return parts.input.value.trim() === '' ? parts.pages : parts.results;
  }

  function paletteOptions(parts) {
    return paletteList(parts).querySelectorAll('[role="option"]');
  }

  function setPaletteOption(parts, index) {
    var list = paletteList(parts);
    parts.input.removeAttribute('aria-activedescendant');
    Array.prototype.forEach.call(paletteOptions(parts), function (option, i) {
      var active = i === index;
      option.setAttribute('aria-selected', active ? 'true' : 'false');
      if (active) {
        option.id = option.id || list.id + '-' + i;
        parts.input.setAttribute('aria-activedescendant', option.id);
        option.scrollIntoView({ block: 'nearest' });
      }
    });
  }

  function renderPalette(parts) {
    var showPages = parts.input.value.trim() === '';
    parts.pages.hidden = !showPages;
    parts.results.hidden = showPages;
    parts.input.setAttribute('aria-controls', paletteList(parts).id);
    setPaletteOption(parts, paletteOptions(parts).length > 0 ? 0 : -1);
  }

  function openPalette() {
    var parts = paletteParts();
    if (!parts || !parts.input || !parts.pages || !parts.results) {
      return;
    }
    if (parts.dialog.open) {
      parts.input.focus();
      return;
    }
    parts.input.value = '';
    parts.results.textContent = '';
    parts.results.removeAttribute('aria-busy');
    renderPalette(parts);
    if (showDialog(parts.dialog)) {
      parts.input.focus();
    }
  }

  function bindPaletteButtons(root) {
    if (!paletteParts()) {
      return;
    }
    each(root, '[data-palette-open]', function (button) {
      button.hidden = false;
    });
  }

  function onPaletteButton(evt) {
    if (evt.target && evt.target.closest && evt.target.closest('[data-palette-open]')) {
      openPalette();
    }
  }

  // Results stay marked busy from a keystroke until the lookup for it lands, so Enter never opens a stale match.
  function onPaletteInput(evt) {
    var parts = paletteParts();
    if (parts && evt.target === parts.input) {
      if (parts.input.value.trim() !== '') {
        parts.results.setAttribute('aria-busy', 'true');
      }
      renderPalette(parts);
    }
  }

  // A lookup for an earlier query can land while the next keystroke's request still waits out its delay;
  // only a response for the query the input shows may fill the results or clear busy.
  function isCurrentPaletteLookup(parts, detail) {
    var config = detail.requestConfig;
    var query = config && config.formData ? config.formData.get('q') : null;
    return String(query == null ? '' : query).trim() === parts.input.value.trim();
  }

  function onPaletteBeforeSwap(evt) {
    var parts = paletteParts();
    var detail = evt.detail || {};
    if (parts && detail.target === parts.results && !isCurrentPaletteLookup(parts, detail)) {
      detail.shouldSwap = false;
    }
  }

  function onPaletteAfterRequest(evt) {
    var parts = paletteParts();
    var detail = evt.detail || {};
    if (parts && detail.target === parts.results && !detail.successful && isCurrentPaletteLookup(parts, detail)) {
      // No swap will clear busy after a network error, which would leave Enter dead.
      parts.results.removeAttribute('aria-busy');
    }
  }

  function onPaletteSwap(evt) {
    var parts = paletteParts();
    if (parts && evt.detail && evt.detail.target === parts.results) {
      parts.results.removeAttribute('aria-busy');
      renderPalette(parts);
    }
  }

  function choosePaletteOption(parts, option) {
    var href = option && option.getAttribute('data-href');
    if (!href) {
      return;
    }
    parts.dialog.close();
    if (option.hasAttribute('data-id')) {
      openMemory(href);
    } else {
      window.location.assign(href);
    }
  }

  function onPaletteKeydown(evt) {
    var parts = paletteParts();
    if (!parts || evt.target !== parts.input || evt.isComposing) {
      return;
    }
    var options = paletteOptions(parts);
    var index = Array.prototype.findIndex.call(options, function (option) {
      return option.getAttribute('aria-selected') === 'true';
    });
    switch (evt.key) {
      case 'ArrowDown':
      case 'ArrowUp':
        evt.preventDefault();
        if (options.length === 0) {
          return;
        }
        if (evt.key === 'ArrowDown') {
          setPaletteOption(parts, index < options.length - 1 ? index + 1 : 0);
        } else {
          setPaletteOption(parts, index > 0 ? index - 1 : options.length - 1);
        }
        return;
      case 'Enter':
        evt.preventDefault();
        if (paletteList(parts) === parts.results && parts.results.getAttribute('aria-busy') === 'true') {
          return;
        }
        choosePaletteOption(parts, options[index === -1 ? 0 : index]);
        return;
    }
  }

  function onPaletteClick(evt) {
    var parts = paletteParts();
    var option = parts && evt.target && evt.target.closest && evt.target.closest('#palette [role="option"]');
    if (option) {
      choosePaletteOption(parts, option);
    }
  }

  // Opening a memory: click its row link when the list shows it, so the row's own htmx wiring (unsaved
  // guard, push-url, row selection) applies. Otherwise htmx.ajax, whose htmx:confirm the guard also sees.

  function pathOf(href) {
    try {
      return new URL(href, window.location.href).pathname;
    } catch (e) {
      return '';
    }
  }

  function rowLinks() {
    return document.querySelectorAll('#memory-rows [data-row-link]');
  }

  function rowLinkFor(href) {
    var path = pathOf(href);
    return Array.prototype.find.call(rowLinks(), function (link) {
      return pathOf(link.getAttribute('href')) === path;
    }) || null;
  }

  function paneMemoryHref() {
    var pane = document.getElementById('detail-pane');
    return pane ? pane.getAttribute('data-memory-href') : null;
  }

  // Marks the row of the memory the pane shows, so rows from a filter refresh or a palette jump agree with it.
  function markPaneRow() {
    var href = paneMemoryHref();
    if (!href) {
      return;
    }
    var open = rowLinkFor(href);
    Array.prototype.forEach.call(rowLinks(), function (link) {
      if (link === open) {
        link.setAttribute('aria-current', 'true');
      } else {
        link.removeAttribute('aria-current');
      }
    });
  }

  function openMemory(href) {
    var link = rowLinkFor(href);
    if (link) {
      link.click();
      return;
    }
    if (!window.htmx || !document.getElementById('detail-pane')) {
      window.location.assign(href);
      return;
    }
    // Keep the list's filter in the URL, as the row links do.
    var url = pathOf(href) + window.location.search;
    window.htmx.ajax('GET', url, { target: '#detail-pane', swap: 'outerHTML', push: url });
  }

  // j / k: the selection moves at once; the memory loads once the keys rest for 150 ms.

  var ROW_LOAD_DELAY_MS = 150;
  var rowLoadTimer = null;
  var pendingRow = null;

  // The row a pending j/k points at, else the open memory's row, else the focused row, else the marked one.
  function currentRowIndex(links) {
    var list = Array.prototype.slice.call(links);
    var pending = list.indexOf(pendingRow);
    if (pending !== -1) {
      return pending;
    }
    var href = paneMemoryHref();
    var open = href ? list.indexOf(rowLinkFor(href)) : -1;
    if (open !== -1) {
      return open;
    }
    var focused = list.indexOf(document.activeElement);
    if (focused !== -1) {
      return focused;
    }
    return list.findIndex(function (link) {
      return link.hasAttribute('aria-current');
    });
  }

  function cancelPendingRow() {
    window.clearTimeout(rowLoadTimer);
    rowLoadTimer = null;
    pendingRow = null;
  }

  function loadPendingRow() {
    var link = pendingRow;
    rowLoadTimer = null;
    pendingRow = null;
    if (!link || !link.isConnected || pathOf(link.getAttribute('href')) === pathOf(paneMemoryHref() || '')) {
      return;
    }
    paneReplacementDeclined = false;
    link.click();
    if (paneReplacementDeclined) {
      // The user kept their edits: put the selection back on the memory that is still open.
      var open = rowLinkFor(paneMemoryHref() || '');
      if (open) {
        open.focus();
      }
    }
  }

  function moveRowSelection(step) {
    var links = rowLinks();
    var index = currentRowIndex(links);
    var next = index === -1 ? 0 : index + step;
    if (next < 0 || next >= links.length) {
      return;
    }
    pendingRow = links[next];
    // A hidden list (narrow layout, Expand) cannot take focus; the memory still loads.
    pendingRow.focus();
    window.clearTimeout(rowLoadTimer);
    rowLoadTimer = window.setTimeout(loadPendingRow, ROW_LOAD_DELAY_MS);
  }

  function focusContent() {
    var content = document.querySelector('#detail-pane textarea[name="content"]');
    if (!content || content.disabled) {
      return false;
    }
    openSectionFold('content');
    content.focus();
    return true;
  }

  // Ctrl+K / Cmd+K open the palette from anywhere; single keys only outside fields and dialogs.
  function navigationShortcut(evt) {
    if (evt.defaultPrevented || evt.altKey || evt.isComposing) {
      return;
    }
    if ((evt.ctrlKey || evt.metaKey) && !evt.shiftKey && String(evt.key).toLowerCase() === 'k') {
      var dialog = openDialog();
      if (!paletteParts()) {
        return;
      }
      evt.preventDefault();
      if (!dialog || dialog.id === 'palette') {
        openPalette();
      }
      return;
    }
    if (evt.ctrlKey || evt.metaKey || openDialog() || isTextEntry(evt.target)) {
      return;
    }
    switch (evt.key) {
      case '/':
        if (paletteParts()) {
          evt.preventDefault();
          openPalette();
        }
        return;
      case '?':
        if (showDialog(document.getElementById('shortcuts'))) {
          evt.preventDefault();
        }
        return;
      case 'j':
      case 'k':
        if (rowLinks().length > 0) {
          evt.preventDefault();
          moveRowSelection(evt.key === 'j' ? 1 : -1);
        }
        return;
      case 'e':
        if (!evt.repeat && focusContent()) {
          evt.preventDefault();
        }
        return;
    }
  }

  applyTheme(storedTheme());
  onReady(bindThemeToggles);
  onReady(bindExpand);
  onReady(bindEditors);
  onReady(bindSectionNav);
  onReady(bindSectionFolds);
  onReady(bindToasts);
  onReady(bindExpiryClear);
  onReady(bindTagChips);
  onReady(bindLookups);
  onReady(bindPaletteButtons);
  document.addEventListener('htmx:configRequest', onLookupConfigRequest);
  document.addEventListener('htmx:afterSwap', onLookupSwap);
  document.addEventListener('htmx:beforeSwap', onPaletteBeforeSwap);
  document.addEventListener('htmx:afterRequest', onPaletteAfterRequest);
  document.addEventListener('htmx:afterSwap', onPaletteSwap);
  document.addEventListener('htmx:afterSettle', markPaneRow);
  // Capture phase: these run before the document-level shortcuts below, which skip prevented events.
  document.addEventListener('keydown', onDialogEscape, true);
  document.addEventListener('keydown', onLinkPeekEscape, true);
  document.addEventListener('mouseout', onLinkPeekLeave);
  document.addEventListener('focusout', onLinkPeekLeave);
  document.addEventListener('keydown', onPaletteKeydown, true);
  document.addEventListener('keydown', onTagKeydown, true);
  document.addEventListener('keydown', onLookupKeydown, true);
  document.addEventListener('submit', onLookupSubmit, true);
  document.addEventListener('submit', guardEditorSubmit, true);
  document.addEventListener('click', guardSaveClick, true);
  document.addEventListener('input', onTagInput);
  document.addEventListener('input', onLookupInput);
  document.addEventListener('focusout', onTagBlur);
  document.addEventListener('focusout', onLookupBlur);
  document.addEventListener('mousedown', onLookupMousedown);
  document.addEventListener('click', onExpiryClear);
  document.addEventListener('input', clearExpiryInvalid);
  document.addEventListener('change', clearExpiryInvalid);
  document.addEventListener('click', onTagRemove);
  document.addEventListener('htmx:confirm', guardPaneReplacement);
  document.addEventListener('input', onEditorInput);
  document.addEventListener('change', onEditorInput);
  document.addEventListener('click', ignoreCleanDiscard, true);
  document.addEventListener('click', onExpandClick);
  document.addEventListener('click', onSectionClick);
  document.addEventListener('click', onSectionToggle);
  document.addEventListener('invalid', openEnclosingFold, true);
  document.addEventListener('beforematch', openEnclosingFold, true);
  document.addEventListener('click', onToastDismiss);
  document.addEventListener('keydown', saveShortcut);
  document.addEventListener('keydown', expandShortcut);
  document.addEventListener('keydown', navigationShortcut);
  document.addEventListener('input', onPaletteInput);
  document.addEventListener('click', onPaletteButton);
  document.addEventListener('click', onPaletteClick);
  document.addEventListener('click', onDialogCloseClick);
  // close does not bubble; the capture phase still sees it on its way to the dialog.
  document.addEventListener('close', onDialogClose, true);
  document.addEventListener('htmx:pushedIntoHistory', rememberUrl);
  document.addEventListener('htmx:replacedInHistory', rememberUrl);
  window.addEventListener('popstate', guardHistory);
  // A cancelled popstate stops at guardHistory; every other one lands here, so lastUrl tracks restores too.
  window.addEventListener('popstate', rememberUrl);
  window.addEventListener('beforeunload', guardUnload);
  window.addEventListener('hashchange', onHashChange);
})();
