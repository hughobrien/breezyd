// SPDX-License-Identifier: GPL-3.0-or-later
//
// Dashboard helper invoked from datastar `data-on-*` expressions in
// cmd/breezyd/ui/templates/controls_block.templ. The helper centralises
// the logic that's too gnarly to inline:
//
//   - the preset-slider snap (1..9 → 0)
//   - match-speeds mirroring (one slider mirrors the other)
//   - implied-mode derivation (sliders → regeneration / supply / extract)
//
// All POSTs are form-encoded for parity with the action handlers'
// r.ParseForm() reads. Responses are SSE event streams; this helper does
// not parse them — the @post() macro from datastar would, but we use
// fetch() here because we want to chain multiple POSTs. Per-card SSE
// patches still arrive via the persistent /ui/sse stream, so the
// dashboard reflects the new state regardless.
window.dashboard = (function () {
  function snapZero(n) { return n > 0 && n < 10 ? 0 : n; }

  function postForm(url, params) {
    var body = Object.keys(params)
      .map(function (k) { return encodeURIComponent(k) + '=' + encodeURIComponent(params[k]); })
      .join('&');
    return fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body,
      credentials: 'same-origin',
    });
  }

  // presetSliderChange handles the change event for one of the two
  // sliders inside a preset editor. card is the .card root; name is the
  // device name; preset is 1/2/3; side is 'supply' or 'extract'; raw is
  // the input's new value (string). signals is { automode, matchSpeeds }.
  function presetSliderChange(card, name, preset, side, raw, signals) {
    var v = snapZero(parseInt(raw, 10));
    var supplyEl = card.querySelector(
      'input[data-side="supply"][data-preset="' + preset + '"]'
    );
    var extractEl = card.querySelector(
      'input[data-side="extract"][data-preset="' + preset + '"]'
    );

    var sup, ext;
    if (signals && signals.matchSpeeds) {
      sup = v;
      ext = v;
      if (supplyEl) supplyEl.value = v;
      if (extractEl) extractEl.value = v;
    } else {
      sup = supplyEl ? parseInt(supplyEl.value, 10) : 0;
      ext = extractEl ? parseInt(extractEl.value, 10) : 0;
      if (side === 'supply') {
        sup = v;
        if (supplyEl) supplyEl.value = v;
      } else {
        ext = v;
        if (extractEl) extractEl.value = v;
      }
    }

    var promises = [];
    if (sup >= 10 && ext >= 10) {
      promises.push(postForm('/ui/devices/' + encodeURIComponent(name) + '/preset', {
        preset: preset,
        supply: sup,
        extract: ext,
      }));
    }

    var implied = null;
    if (signals && signals.automode) implied = 'ventilation';
    else if (sup >= 10 && ext >= 10) implied = 'regeneration';
    else if (sup === 0 && ext >= 10) implied = 'extract';
    else if (sup >= 10 && ext === 0) implied = 'supply';
    if (
      implied &&
      card.getAttribute('data-speed-mode') === 'preset' + preset &&
      card.getAttribute('data-airflow-mode') !== implied
    ) {
      promises.push(postForm('/ui/devices/' + encodeURIComponent(name) + '/mode', {
        mode: implied,
      }));
    }

    return Promise.all(promises);
  }

  return { presetSliderChange: presetSliderChange };
})();
