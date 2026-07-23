// zima-vm-extras frontend
// The daemon binds 127.0.0.1 only and registers a gateway route, so the
// API is always reachable same-origin (port 80) under that route — whether
// the page is the dashboard module (/modules/zima_vm_extras/) or proxied
// directly (/v2/vm_extras/). No cross-origin, no CORS.

const API_BASE = (() => {
  if (location.pathname.startsWith('/modules/') ||
      location.pathname.startsWith('/v2/vm_extras')) {
    return '/v2/vm_extras/api';
  }
  // Direct access to the daemon itself (dev/debug on 127.0.0.1).
  return './api';
})();

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const debounce = (fn, ms) => {
  let t;
  return (...a) => { clearTimeout(t); t = setTimeout(() => fn(...a), ms); };
};

function setStatus(msg, kind = '') {
  const el = $('#status');
  if (!el) return;
  el.textContent = msg;
  el.className = kind;
  if (msg) setTimeout(() => { if (el.textContent === msg) setStatus(''); }, 3500);
}

async function api(path, opts = {}) {
  const r = await fetch(API_BASE + path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || `HTTP ${r.status}`);
  return data;
}

// Show the daemon's build version in the header badge.
async function loadVersion() {
  try {
    const h = await api('/health');
    const el = $('#app-version');
    if (el && h && h.version) el.textContent = 'v' + h.version;
  } catch (e) { /* version badge is non-critical */ }
}

function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/[&<>"']/g, c =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function stateClass(state) {
  if (state === 'running') return 'state-running';
  if (state === 'shut off' || state === 'shutoff') return 'state-shut';
  return 'state-other';
}

// Friendly label: prefer human title, else libvirt name.
function vmLabel(v) {
  return v.title && v.title.trim() ? v.title : v.name;
}

let vmCache = [];
let storageTargets = [];

function fmtBytes(n) {
  if (!n) return '?';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0; let v = n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + u[i];
}

async function loadStorageTargets() {
  try {
    const r = await api('/storage/targets');
    storageTargets = r.data || [];
  } catch (e) {
    storageTargets = [];
  }
  populateStorageSelect();
}

function populateStorageSelect() {
  const sel = $('#snap-storage-select');
  if (!sel) return;
  const prev = sel.value;
  sel.innerHTML = '';
  for (const t of storageTargets) {
    const opt = document.createElement('option');
    opt.value = t.path;
    const free = fmtBytes(t.avail_bytes);
    const total = fmtBytes(t.total_bytes);
    const remote = t.is_remote ? ' · remote' : '';
    const suggested = t.suggested ? ' · default' : '';
    opt.textContent = `${t.path} (${t.fstype}, ${free} free / ${total}${remote}${suggested})`;
    sel.appendChild(opt);
  }
  // Custom option always last
  const custom = document.createElement('option');
  custom.value = '__custom__';
  custom.textContent = '— Custom path —';
  sel.appendChild(custom);
  // Restore previous selection if still present
  if (prev && Array.from(sel.options).some(o => o.value === prev)) sel.value = prev;
}

async function loadVMs() {
  const { data } = await api('/vms');
  vmCache = data || [];
  renderStatusBar();
  populateVMSelects();
  return vmCache;
}

function renderStatusBar() {
  $('#stat-total').textContent = vmCache.length;
  $('#stat-running').textContent = vmCache.filter(v => v.state === 'running').length;
  $('#stat-autostart').textContent = vmCache.filter(v => v.libvirt_autostart || v.enabled).length;
}

function populateVMSelects() {
  for (const id of ['snap-vm-select', 'usb-vm-select', 'pci-vm-select', 'vnc-vm-select', 'tpm-vm-select', 'metrics-vm-select', 'backup-vm-select', 'net-vm-select']) {
    const sel = $('#' + id);
    if (!sel) continue;
    const prev = sel.value;
    sel.innerHTML = '';
    for (const v of vmCache) {
      const opt = document.createElement('option');
      opt.value = v.name;
      const label = vmLabel(v);
      const suffix = label === v.name ? '' : ` (${v.name})`;
      opt.textContent = `${label}${suffix} · ${v.state}${v.has_uefi ? ' · UEFI' : ''}`;
      sel.appendChild(opt);
    }
    if (prev && vmCache.find(v => v.name === prev)) sel.value = prev;
  }
}

// ---------- POWER ----------

// Which buttons make sense depends on the state libvirt reports. "Force off"
// is the plug-pull and always asks first; "Shut down" and "Reboot" are ACPI
// requests a guest is free to ignore, so the row is re-read afterwards rather
// than assuming the VM obeyed.
const POWER_ACTIONS = {
  'shut off': [{ act: 'start', label: 'Start', cls: 'btn-primary' }],
  'shutoff': [{ act: 'start', label: 'Start', cls: 'btn-primary' }],
  'running': [
    { act: 'shutdown', label: 'Shut down', cls: 'btn-secondary', stops: true },
    { act: 'reboot', label: 'Reboot', cls: 'btn-secondary' },
    { act: 'force-off', label: 'Force off', cls: 'btn-danger', confirm: true, stops: true },
  ],
  'paused': [
    { act: 'resume', label: 'Resume', cls: 'btn-primary' },
    { act: 'force-off', label: 'Force off', cls: 'btn-danger', confirm: true, stops: true },
  ],
};

function powerControls(vm) {
  const wrap = document.createElement('div');
  wrap.className = 'vm-power';
  const actions = POWER_ACTIONS[vm.state] || [];
  if (!actions.length) {
    wrap.innerHTML = `<span class="vm-power-none" title="No power action available in state '${escapeHtml(vm.state || '')}'">—</span>`;
    return wrap;
  }
  for (const a of actions) {
    const btn = document.createElement('button');
    btn.className = a.cls + ' btn-sm';
    btn.textContent = a.label;
    btn.addEventListener('click', async () => {
      if (a.confirm && !confirm(
        `Force off "${vmLabel(vm)}"?\n\nThis cuts power immediately — the guest gets no chance ` +
        `to flush its filesystems, exactly like pulling the plug.`)) return;
      // A VM whose watchdog is on gets restarted within ~30 s of reaching
      // "shut off", so a stop request here would appear to do nothing. Say so
      // before the click rather than letting the user rediscover it.
      if (a.stops && vm.watchdog && !confirm(
        `"${vmLabel(vm)}" has its watchdog enabled — VM Extras will restart it within ` +
        `about 30 seconds of it shutting down.\n\nStop it anyway? Turn the watchdog off ` +
        `first if you want it to stay down.`)) return;
      const siblings = Array.from(wrap.querySelectorAll('button'));
      siblings.forEach(b => { b.disabled = true; });
      try {
        const r = await api(`/power/${encodeURIComponent(vm.name)}`, {
          method: 'POST',
          body: JSON.stringify({ action: a.act }),
        });
        // The reply carries the state as it is right now, which after an ACPI
        // request is usually still the old one — say so instead of implying
        // the VM already stopped.
        setStatus(
          (a.act === 'shutdown' || a.act === 'reboot')
            ? `${a.label} requested for ${vmLabel(vm)} — now: ${r.state}`
            : `${vmLabel(vm)} is now: ${r.state}`,
          'ok');
        setTimeout(renderAutostartTab, 1200);
      } catch (e) {
        setStatus(`${a.label} failed: ${e.message}`, 'error');
        siblings.forEach(b => { b.disabled = false; });
      }
    });
    wrap.appendChild(btn);
  }
  return wrap;
}

// ---------- AUTOSTART ----------

function autostartCard(vm) {
  const card = document.createElement('div');
  card.className = 'vm-card';
  card.dataset.name = vm.name;

  const label = vmLabel(vm);
  const sublabel = label === vm.name ? '' : `<div class="vm-sub">${escapeHtml(vm.name)}</div>`;

  const info = document.createElement('div');
  info.className = 'vm-info';
  info.innerHTML = `
    <div class="vm-name">${escapeHtml(label)}
      <span class="vm-state ${stateClass(vm.state)}">${escapeHtml(vm.state || 'unknown')}</span>
      ${vm.has_uefi ? '<span class="badge">UEFI</span>' : ''}
    </div>
    ${sublabel}
    <div class="vm-meta">libvirt autostart flag: <strong>${vm.libvirt_autostart ? 'on' : 'off'}</strong></div>
  `;

  const ctrl = document.createElement('div');
  ctrl.className = 'vm-controls';

  const toggleWrap = document.createElement('label');
  toggleWrap.className = 'switch';
  toggleWrap.title = 'Autostart on host boot';
  toggleWrap.innerHTML = `<input type="checkbox" ${vm.enabled ? 'checked' : ''}><span class="slider"></span>`;
  const cb = toggleWrap.querySelector('input');

  const wdField = document.createElement('label');
  wdField.className = 'checkbox';
  wdField.title = 'Watchdog: restart this VM automatically if it stops or crashes';
  wdField.innerHTML = `<input type="checkbox" ${vm.watchdog ? 'checked' : ''}> Watchdog`;
  const wdCb = wdField.querySelector('input');

  const orderField = document.createElement('div');
  orderField.className = 'field';
  orderField.innerHTML = `
    <label title="Lower number starts first. Example: database=10, app=20.">Order</label>
    <input type="number" min="0" max="999" value="${vm.order || 0}" title="Lower number starts first (0–999)">
  `;
  const orderInput = orderField.querySelector('input');

  const delayField = document.createElement('div');
  delayField.className = 'field';
  delayField.innerHTML = `
    <label title="Seconds to wait before starting the next VM.">Delay (s)</label>
    <input type="number" min="0" max="600" value="${vm.delay_s || 0}" title="Seconds to wait">
  `;
  const delayInput = delayField.querySelector('input');

  ctrl.append(powerControls(vm), toggleWrap, wdField, orderField, delayField);
  card.append(info, ctrl);

  const save = debounce(async () => {
    try {
      await api(`/autostart/${encodeURIComponent(vm.name)}`, {
        method: 'PUT',
        body: JSON.stringify({
          enabled: cb.checked,
          watchdog: wdCb.checked,
          order: parseInt(orderInput.value, 10) || 0,
          delay_s: parseInt(delayInput.value, 10) || 0,
        }),
      });
      setStatus(`Saved: ${label}`, 'ok');
      vm.enabled = cb.checked;
      vm.watchdog = wdCb.checked;
      vm.libvirt_autostart = false; // VM Extras orchestrates; native flag stays off
      vm.order = parseInt(orderInput.value, 10) || 0;
      vm.delay_s = parseInt(delayInput.value, 10) || 0;
      renderStatusBar();
    } catch (e) {
      setStatus(`Save failed: ${e.message}`, 'error');
      cb.checked = vm.enabled;
    }
  }, 250);

  cb.addEventListener('change', save);
  wdCb.addEventListener('change', save);
  orderInput.addEventListener('change', save);
  delayInput.addEventListener('change', save);

  return card;
}

async function renderAutostartTab() {
  const list = $('#autostart-list');
  list.innerHTML = '<div class="loading">Loading…</div>';
  try {
    await loadVMs();
    list.innerHTML = '';
    if (vmCache.length === 0) {
      list.innerHTML = '<div class="empty">No VMs found. Create one via the ZVM app.</div>';
      return;
    }
    for (const vm of vmCache) list.appendChild(autostartCard(vm));
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

// ---------- SNAPSHOTS ----------

// The last value this code auto-filled into #snap-extdir. Anything else in
// that field was typed or picked by the operator and is never overwritten.
let snapExtDirAuto = '';

async function renderSnapshotsForCurrentVM() {
  const list = $('#snap-list');
  const sel = $('#snap-vm-select');
  const meta = $('#snap-vm-meta');
  if (!sel.value) {
    list.innerHTML = '<div class="empty">No VM selected.</div>';
    return;
  }
  loadScheduleForCurrentVM();
  list.innerHTML = '<div class="loading">Loading snapshots…</div>';
  try {
    const r = await api(`/snapshot/${encodeURIComponent(sel.value)}`);
    const extBox = $('#snap-external');
    const extDirInput = $('#snap-extdir');
    if (r.external_required) {
      extBox.checked = true;
      extBox.disabled = true;
      meta.textContent = `state: ${r.state} · full snapshot (disk + memory, revertable)`;
    } else {
      extBox.checked = false;
      extBox.disabled = true;
      meta.textContent = `state: ${r.state} · internal snapshot (revertable)`;
    }
    if (r.default_external_dir) {
      extDirInput.placeholder = r.default_external_dir;
      // The target directory ends in the VM's own name, so it has to follow
      // the VM picker. The old `if (!value)` left the previously selected
      // VM's path in the field — switch from A to B and B's overlay files
      // would have been written into A's directory. A path the operator typed
      // themselves is still left alone: only a value this code put there is
      // replaced.
      if (!extDirInput.value || extDirInput.value === snapExtDirAuto) {
        extDirInput.value = r.default_external_dir;
        snapExtDirAuto = r.default_external_dir;
      }
    }
    // Sync the storage dropdown to the prefix of the current path (if it matches a known mount).
    syncStorageSelectionFromPath();
    // If a real mount is selected, recompose the path for the VM now shown —
    // the per-VM subdirectory has to change with the picker, and without a
    // focus steal, since this runs on render rather than on a click.
    const storageSel = $('#snap-storage-select');
    if (storageSel && storageSel.value && storageSel.value !== '__custom__'
        && (extDirInput.value === snapExtDirAuto || !extDirInput.value)) {
      applyStorageSelection(false);
    }
    updateExtDirVisibility();
    list.innerHTML = '';
    if (!r.data || r.data.length === 0) {
      list.innerHTML = '<div class="empty">No snapshots yet.</div>';
      return;
    }
    for (const s of r.data) list.appendChild(snapshotRow(sel.value, s, r.current));
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function snapshotRow(vm, snap, currentName) {
  const row = document.createElement('div');
  row.className = 'snap-row card';
  const isCurrent = snap.name === currentName;
  row.innerHTML = `
    <div class="snap-info">
      <div class="snap-name">${escapeHtml(snap.name)} ${isCurrent ? '<span class="badge badge-current">current</span>' : ''}</div>
      <div class="snap-meta">created: ${escapeHtml(snap.created_at || '-')} · state: ${escapeHtml(snap.state || '-')}${snap.parent ? ' · parent: ' + escapeHtml(snap.parent) : ''}</div>
    </div>
    <div class="snap-actions">
      <button class="btn-secondary" data-act="revert">Revert</button>
      <button class="btn-danger" data-act="delete">Delete</button>
    </div>
  `;
  row.querySelector('[data-act="revert"]').addEventListener('click', async () => {
    if (!confirm(`Revert VM "${vm}" to snapshot "${snap.name}"?`)) return;
    try {
      await api(`/snapshot/${encodeURIComponent(vm)}/${encodeURIComponent(snap.name)}/revert`, {
        method: 'POST',
        body: JSON.stringify({ force: false }),
      });
      setStatus(`Revert ok: ${snap.name}`, 'ok');
      renderSnapshotsForCurrentVM();
    } catch (e) {
      setStatus(`Revert failed: ${e.message}`, 'error');
    }
  });
  row.querySelector('[data-act="delete"]').addEventListener('click', async () => {
    if (!confirm(`Delete snapshot "${snap.name}"?`)) return;
    try {
      await api(`/snapshot/${encodeURIComponent(vm)}/${encodeURIComponent(snap.name)}`, { method: 'DELETE' });
      setStatus(`Deleted: ${snap.name}`, 'ok');
      renderSnapshotsForCurrentVM();
    } catch (e) {
      setStatus(`Delete failed: ${e.message}`, 'error');
    }
  });
  return row;
}

async function renderSnapshotsTab() {
  await loadVMs();
  await loadStorageTargets();
  const sel = $('#snap-vm-select');
  if (!sel.value && vmCache.length) sel.value = vmCache[0].name;
  await renderSnapshotsForCurrentVM();
}

// The storage target only applies to external snapshots, i.e. to a VM that is
// running or paused. v0.6.3 hid the whole row for a shut-off VM, which read as
// "the target picker is broken" rather than "there is nothing to target".
// The row now stays put and explains itself.
function updateExtDirVisibility() {
  const row = $('#snap-extdir-row');
  if (!row) return;
  const external = $('#snap-external').checked;
  const note = $('#snap-extdir-note');
  const sel = $('#snap-storage-select');
  const ext = $('#snap-extdir');

  if (sel) sel.disabled = !external;
  if (ext) ext.disabled = !external;
  row.classList.toggle('is-disabled', !external);
  if (note) {
    note.textContent = external
      ? ''
      : 'This VM is shut off, so the snapshot is stored inside its own qcow2 image (internal snapshot). '
        + 'No separate target directory is used. Start or pause the VM to take a full external snapshot instead.';
  }
}

// When the user picks a storage target, rewrite extDirInput to that mount + /zima-vm-extras-snapshots/<vm>.
function applyStorageSelection(focusOnCustom = true) {
  const sel = $('#snap-storage-select');
  const ext = $('#snap-extdir');
  const vm = $('#snap-vm-select').value;
  if (!sel || !ext) return;
  if (sel.value === '__custom__') {
    ext.removeAttribute('readonly');
    if (focusOnCustom) ext.focus();
    return;
  }
  if (sel.value) {
    const base = sel.value.replace(/\/+$/, '');
    ext.value = `${base}/zima-vm-extras-snapshots/${vm || ''}`.replace(/\/+$/, '/');
    if (!vm) ext.value = `${base}/zima-vm-extras-snapshots/`;
    // Composed by us, so a later VM switch may recompose it.
    snapExtDirAuto = ext.value;
    ext.setAttribute('readonly', 'readonly');
  }
}

// Best-effort: find the longest matching mountpoint prefix and select it in the dropdown.
function syncStorageSelectionFromPath() {
  const ext = $('#snap-extdir');
  const sel = $('#snap-storage-select');
  if (!ext || !sel || !storageTargets.length) return;
  const path = ext.value || ext.placeholder || '';
  let best = '';
  for (const t of storageTargets) {
    if (path === t.path || path.startsWith(t.path + '/')) {
      if (t.path.length > best.length) best = t.path;
    }
  }
  if (best && Array.from(sel.options).some(o => o.value === best)) {
    sel.value = best;
    ext.setAttribute('readonly', 'readonly');
  } else {
    sel.value = '__custom__';
    ext.removeAttribute('readonly');
  }
}

// ---------- SNAPSHOT SCHEDULE ----------

async function loadScheduleForCurrentVM() {
  const vm = $('#snap-vm-select').value;
  if (!vm) return;
  try {
    const e = await api(`/schedule/${encodeURIComponent(vm)}`);
    $('#sched-enabled').checked = !!e.enabled;
    if (e.interval_hours > 0) { // an existing schedule — show its real values
      $('#sched-interval').value = e.interval_hours;
      $('#sched-keep').value = e.keep;
    } else {                    // no schedule yet — show sensible defaults
      $('#sched-interval').value = 24;
      $('#sched-keep').value = 7;
    }
    const st = $('#sched-status');
    st.textContent = e.last_run_unix
      ? `Last automatic snapshot: ${new Date(e.last_run_unix * 1000).toLocaleString()}`
      : 'Scheduled snapshots are named auto-…; retention only prunes those.';
  } catch (err) { /* schedule info is non-critical */ }
}

// ---------- USB ----------

async function renderUSBTab() {
  await loadVMs();
  const list = $('#usb-host-list');
  list.innerHTML = '<div class="loading">Loading USB devices…</div>';
  try {
    const vm = $('#usb-vm-select').value;
    let pinned = new Set();
    if (vm) {
      try {
        const p = await api(`/usb/${encodeURIComponent(vm)}/pinned`);
        pinned = new Set((p.data || []).map(d => `${d.vendor_id}:${d.product_id}`));
      } catch (e) { /* pinned info is non-critical */ }
    }
    const r = await api('/usb/host');
    list.innerHTML = '';
    if (!r.data || r.data.length === 0) {
      list.innerHTML = '<div class="empty">No USB devices found.</div>';
      return;
    }
    for (const dev of r.data) list.appendChild(usbRow(dev, pinned));
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function isLikelyRootHub(dev) {
  return /root hub/i.test(dev.description || '');
}

function usbRow(dev, pinned) {
  const row = document.createElement('div');
  row.className = 'usb-row card';
  if (isLikelyRootHub(dev)) row.classList.add('muted');
  const isPinned = pinned && pinned.has(`${dev.vendor_id}:${dev.product_id}`);
  row.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">${escapeHtml(dev.vendor_id)}:${escapeHtml(dev.product_id)}
        ${isLikelyRootHub(dev) ? '<span class="badge">root hub</span>' : ''}
        ${isPinned ? '<span class="badge badge-current">pinned</span>' : ''}
      </div>
      <div class="usb-desc">${escapeHtml(dev.description || '(no name)')}</div>
      <div class="usb-meta">Bus ${escapeHtml(dev.bus)} · Device ${escapeHtml(dev.device_id)}</div>
    </div>
    <div class="usb-actions">
      <label class="checkbox" title="Persistent: pinned in the VM config and kept attached across ZVM re-saves and host reboots.">
        <input type="checkbox" class="usb-persistent" checked> Persistent
      </label>
      <button class="btn-primary" data-act="attach">Attach</button>
      <button class="btn-secondary" data-act="detach">Detach</button>
    </div>
  `;
  const persistentBox = row.querySelector('.usb-persistent');
  row.querySelector('[data-act="attach"]').addEventListener('click', async () => {
    const vm = $('#usb-vm-select').value;
    if (!vm) { setStatus('No target VM selected', 'error'); return; }
    if (!confirm(`Attach USB device ${dev.vendor_id}:${dev.product_id} (${dev.description}) to VM "${vm}"?`)) return;
    try {
      await api(`/usb/${encodeURIComponent(vm)}`, {
        method: 'POST',
        body: JSON.stringify({
          vendor_id: dev.vendor_id,
          product_id: dev.product_id,
          persistent: persistentBox.checked,
          description: dev.description || '',
        }),
      });
      setStatus(`Attached${persistentBox.checked ? ' & pinned' : ''}: ${dev.vendor_id}:${dev.product_id}`, 'ok');
      renderUSBTab();
    } catch (e) {
      setStatus(`Attach failed: ${e.message}`, 'error');
    }
  });
  row.querySelector('[data-act="detach"]').addEventListener('click', async () => {
    const vm = $('#usb-vm-select').value;
    if (!vm) { setStatus('No target VM selected', 'error'); return; }
    try {
      await api(`/usb/${encodeURIComponent(vm)}/${encodeURIComponent(dev.vendor_id)}:${encodeURIComponent(dev.product_id)}`, {
        method: 'DELETE',
      });
      setStatus(`Detached: ${dev.vendor_id}:${dev.product_id}`, 'ok');
      renderUSBTab();
    } catch (e) {
      setStatus(`Detach failed: ${e.message}`, 'error');
    }
  });
  return row;
}

// ---------- PCIe ----------

async function renderPCITab() {
  await loadVMs();
  const list = $('#pci-host-list');
  list.innerHTML = '<div class="loading">Loading PCI devices…</div>';
  try {
    const vm = $('#pci-vm-select').value;
    let pinned = new Set();
    if (vm) {
      try {
        const p = await api(`/pci/${encodeURIComponent(vm)}/pinned`);
        pinned = new Set((p.data || []).map(d => d.address));
      } catch (e) { /* pinned info is non-critical */ }
    }
    const r = await api('/pci/host');
    list.innerHTML = '';
    if (!r.data || r.data.length === 0) {
      list.innerHTML = '<div class="empty">No PCI devices found.</div>';
      return;
    }

    // A typical AMD host lists dozens of bridges and root complexes that can
    // never be passed through — they drowned the handful of devices that can.
    // They are hidden by default, but only ever hidden, never dropped: the
    // count says how many and one click brings them back. Anything already
    // pinned to a VM stays visible whatever the filter says.
    const showAll = $('#pci-show-all').checked;
    const hasGroup = d => d.iommu_group !== '' && d.iommu_group != null;
    // VFIO passthrough needs an IOMMU group, so a device without one is not a
    // candidate either. The guard matters: if IOMMU is switched off in the
    // firmware, *no* device has a group — filtering on that alone would empty
    // the list and hide the real problem. So the group rule only applies when
    // at least one device does have a group.
    const anyGrouped = r.data.some(hasGroup);
    const attachable = d => d.passable && (!anyGrouped || hasGroup(d));
    const shown = r.data.filter(d => showAll || attachable(d) || pinned.has(d.address));
    const hidden = r.data.length - shown.length;

    const note = $('#pci-filter-note');
    if (note) {
      if (!anyGrouped) {
        note.textContent = 'No device has an IOMMU group — IOMMU/VT-d looks disabled in the host firmware, '
          + 'so nothing can be passed through until it is enabled.';
      } else {
        note.textContent = hidden > 0
          ? `${hidden} of ${r.data.length} devices hidden — bridges and devices without an IOMMU group, which cannot be passed through.`
          : '';
      }
    }
    if (shown.length === 0) {
      list.innerHTML = '<div class="empty">No passable PCI devices on this host.</div>';
      return;
    }
    for (const dev of shown) list.appendChild(pciRow(dev, pinned));
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function pciRow(dev, pinned) {
  const row = document.createElement('div');
  row.className = 'usb-row card';
  if (!dev.passable) row.classList.add('muted');
  const isPinned = pinned && pinned.has(dev.address);
  const grp = (dev.iommu_group !== '' && dev.iommu_group != null)
    ? `IOMMU group ${escapeHtml(dev.iommu_group)}` : 'no IOMMU group';
  const drv = dev.driver ? `host driver: ${escapeHtml(dev.driver)}` : 'no host driver';
  row.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">${escapeHtml(dev.address)}
        <span class="usb-meta">[${escapeHtml(dev.vendor_id)}:${escapeHtml(dev.device_id)}]</span>
        ${!dev.passable ? '<span class="badge">bridge — not passable</span>' : ''}
        ${isPinned ? '<span class="badge badge-current">pinned</span>' : ''}
      </div>
      <div class="usb-desc">${escapeHtml(dev.description || dev.class_name || '(unknown)')}</div>
      <div class="usb-meta">${escapeHtml(dev.class_name || '')} · ${grp} · ${drv}</div>
    </div>
    <div class="usb-actions">
      <label class="checkbox" title="Persistent: pinned in the VM config and kept across ZVM re-saves and host reboots.">
        <input type="checkbox" class="pci-persistent" checked> Persistent
      </label>
      <button class="btn-primary" data-act="attach"${dev.passable ? '' : ' disabled'}>Attach</button>
      <button class="btn-secondary" data-act="detach">Detach</button>
    </div>
  `;
  const persistentBox = row.querySelector('.pci-persistent');
  const attachBtn = row.querySelector('[data-act="attach"]');
  if (attachBtn && !attachBtn.disabled) {
    attachBtn.addEventListener('click', async () => {
      const vm = $('#pci-vm-select').value;
      if (!vm) { setStatus('No target VM selected', 'error'); return; }
      if (!confirm(`Pass PCI device ${dev.address} (${dev.description}) through to VM "${vm}"?\n\nMake sure the host does not need this device — and that its whole IOMMU group can go to this VM.`)) return;
      try {
        await api(`/pci/${encodeURIComponent(vm)}`, {
          method: 'POST',
          body: JSON.stringify({
            address: dev.address,
            persistent: persistentBox.checked,
            description: dev.description || '',
          }),
        });
        setStatus(`Attached${persistentBox.checked ? ' & pinned' : ''}: ${dev.address}`, 'ok');
        renderPCITab();
      } catch (e) {
        setStatus(`Attach failed: ${e.message}`, 'error');
      }
    });
  }
  row.querySelector('[data-act="detach"]').addEventListener('click', async () => {
    const vm = $('#pci-vm-select').value;
    if (!vm) { setStatus('No target VM selected', 'error'); return; }
    try {
      await api(`/pci/${encodeURIComponent(vm)}/${encodeURIComponent(dev.address)}`, { method: 'DELETE' });
      setStatus(`Detached: ${dev.address}`, 'ok');
      renderPCITab();
    } catch (e) {
      setStatus(`Detach failed: ${e.message}`, 'error');
    }
  });
  return row;
}

// ---------- VNC SECURITY ----------

async function renderVNCTab() {
  await loadVMs();
  const sel = $('#vnc-vm-select');
  if (!sel.value && vmCache.length) sel.value = vmCache[0].name;
  await renderVNCForCurrentVM();
}

async function renderVNCForCurrentVM() {
  const body = $('#vnc-body');
  const vm = $('#vnc-vm-select').value;
  if (!vm) { body.innerHTML = '<div class="empty">No VM selected.</div>'; return; }
  body.innerHTML = '<div class="loading">Loading…</div>';
  try {
    const st = await api(`/vnc/${encodeURIComponent(vm)}`);
    body.innerHTML = '';
    body.appendChild(vncCard(vm, st));
    if (st.present) body.appendChild(vncReachCard(vm, st));
  } catch (e) {
    body.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

// ---------- TPM & SECURE BOOT ----------

async function renderTPMTab() {
  await loadVMs();
  const sel = $('#tpm-vm-select');
  if (!sel.value && vmCache.length) sel.value = vmCache[0].name;
  await renderTPMForCurrentVM();
}

async function renderTPMForCurrentVM() {
  const body = $('#tpm-body');
  const vm = $('#tpm-vm-select').value;
  if (!vm) { body.innerHTML = '<div class="empty">No VM selected.</div>'; return; }
  body.innerHTML = '<div class="loading">Loading…</div>';
  try {
    const st = await api(`/tpm/${encodeURIComponent(vm)}`);
    body.innerHTML = '';
    body.appendChild(tpmCard(vm, st));
    body.appendChild(firmwareCard(st));
  } catch (e) {
    body.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function tpmCard(vm, st) {
  const card = document.createElement('div');
  card.className = 'usb-row card';

  // Same three states as the VNC tab, for the same reason: the device is
  // written to the persistent config, which the running qemu never re-reads.
  // Reporting "TPM active" while the running guest has none would send the
  // operator back to a Windows installer that still refuses.
  let badge, desc;
  if (st.live_error) {
    badge = '<span class="badge badge-danger">live state unknown</span>';
    desc = 'Could not read the running VM\'s devices: ' + escapeHtml(st.live_error);
  } else if (st.restart_required) {
    badge = '<span class="badge badge-warn">restart required</span>';
    desc = 'The TPM is saved in the VM config, but the guest running right now ' +
      'started without one and still fails the Windows 11 check. Restart the VM.';
  } else if (st.present) {
    badge = '<span class="badge badge-current">TPM present</span>';
    desc = `The VM has a TPM device (${escapeHtml(st.model || 'unknown model')}` +
      `${st.version ? ', version ' + escapeHtml(st.version) : ''}).`;
  } else {
    badge = '<span class="badge badge-danger">no TPM</span>';
    desc = 'This VM has no TPM device — Windows 11 will refuse to install.';
  }

  card.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">TPM ${badge}</div>
      <div class="usb-desc">${desc}</div>
      <div class="usb-meta">${st.pinned ? 'pinned by VM Extras' : 'not pinned'}${st.running ? ' · VM running' : ' · VM not running'}</div>
    </div>
    <div class="usb-actions">
      ${st.present
        ? '<button class="btn-danger" data-act="remove">Remove TPM</button>'
        : '<button class="btn-primary" data-act="add">Add TPM 2.0</button>'}
    </div>
  `;

  const addBtn = card.querySelector('[data-act="add"]');
  if (addBtn) addBtn.addEventListener('click', async () => {
    addBtn.disabled = true;
    try {
      const r = await api(`/tpm/${encodeURIComponent(vm)}`, { method: 'POST' });
      setStatus(r.note ? `TPM 2.0 added — ${r.note}` : 'TPM 2.0 added', 'ok');
      renderTPMForCurrentVM();
    } catch (e) {
      setStatus('Adding the TPM failed: ' + e.message, 'error');
      addBtn.disabled = false;
    }
  });

  const rmBtn = card.querySelector('[data-act="remove"]');
  if (rmBtn) rmBtn.addEventListener('click', async () => {
    rmBtn.disabled = true;
    try {
      await api(`/tpm/${encodeURIComponent(vm)}`, { method: 'DELETE' });
      setStatus('TPM removed — takes effect on next VM start', 'ok');
      renderTPMForCurrentVM();
    } catch (e) {
      setStatus('Removing the TPM failed: ' + e.message, 'error');
      rmBtn.disabled = false;
    }
  });

  return card;
}

// Secure Boot is reported, never switched — see the note in the panel hint.
function firmwareCard(st) {
  const card = document.createElement('div');
  card.className = 'usb-row card';

  let badge, desc;
  if (st.firmware_error) {
    badge = '<span class="badge badge-danger">firmware unknown</span>';
    desc = 'Could not read the VM\'s firmware: ' + escapeHtml(st.firmware_error);
  } else if (!st.uefi) {
    badge = '<span class="badge badge-danger">legacy BIOS</span>';
    desc = 'This VM boots legacy BIOS. Windows 11 requires UEFI, so no TPM will ' +
      'make it install here — the VM has to be recreated with UEFI firmware.';
  } else if (st.secure_boot && st.enrolled_keys) {
    badge = '<span class="badge badge-current">secure boot active</span>';
    desc = 'The VM uses a Secure Boot firmware with keys enrolled, so it ' +
      'actually validates what it boots.';
  } else if (st.secure_boot) {
    // ZimaOS pairs its Windows 11 firmware with the *empty* edk2-i386-vars.fd
    // and ships no vars file with Microsoft's keys, so Secure Boot is switched
    // on with nothing to check against. Calling that plain "secure boot" would
    // be a green badge over a firmware in setup mode.
    badge = '<span class="badge badge-warn">secure boot, no keys</span>';
    desc = 'The firmware is Secure Boot capable, but no keys are enrolled, so ' +
      'it stays in setup mode and reports Secure Boot as <b>off</b> to the ' +
      'guest — Windows shows "Secure Boot State: Off" in msinfo32. Windows 11 ' +
      'still installs, because setup checks for Secure Boot capability rather ' +
      'than for it being active. ZimaOS ships no NVRAM template with keys ' +
      'enrolled, so this cannot be fixed from here.';
  } else {
    badge = '<span class="badge badge-warn">UEFI, no secure boot</span>';
    desc = 'The VM uses UEFI but the plain firmware image, which cannot enforce ' +
      'Secure Boot. ZimaOS ships a Secure Boot image next to it, but switching ' +
      'an existing VM to it needs a fresh NVRAM file — doing that here could ' +
      'leave the VM unable to boot, so it is not offered.';
  }

  card.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">Firmware ${badge}</div>
      <div class="usb-desc">${desc}</div>
      <div class="usb-meta">${st.loader ? 'loader: ' + escapeHtml(st.loader) : 'no UEFI loader'}</div>
    </div>
  `;
  return card;
}

// A listen address that answers on every interface. ZVM writes '::', which is
// the IPv6 any-address and accepts IPv4 too — the same reach as '0.0.0.0'.
function vncListenIsExposed(addr) {
  return addr === '' || addr === '::' || addr === '0.0.0.0' || addr === '*';
}

function vncListenLabel(addr) {
  if (addr === '127.0.0.1' || addr === '::1') return 'local only';
  if (vncListenIsExposed(addr)) return 'all interfaces';
  return addr;
}

// Reachability is the other half of console security, and the half ZVM gets
// wrong by default: a password stops someone using the console, the listen
// address stops them reaching it at all. Restricting it to localhost turns the
// console into something only a tunnel or a reverse proxy on the box can open.
function vncReachCard(vm, st) {
  const card = document.createElement('div');
  card.className = 'usb-row card';

  const exposed = vncListenIsExposed(st.listen);
  let badge, desc;
  if (st.listen_restart_required) {
    // Same trap as the password: `virsh define` edits the persistent config,
    // and the running qemu keeps the socket it was started with.
    badge = '<span class="badge badge-warn">restart required</span>';
    desc = `The VM config now says <b>${escapeHtml(vncListenLabel(st.listen))}</b>, but the console ` +
      `running right now is still bound to <b>${escapeHtml(vncListenLabel(st.live_listen))}</b>` +
      `${st.live_port ? ' on port ' + st.live_port : ''}. Restart the VM to apply it.`;
  } else if (exposed) {
    badge = '<span class="badge badge-warn">reachable from the LAN</span>';
    desc = 'The console accepts connections on every interface. That is fine with a password ' +
      'set, but restricting it to localhost means only this machine — an SSH tunnel or a ' +
      'reverse proxy running on the ZimaOS box — can reach it at all.';
  } else {
    badge = '<span class="badge badge-current">local only</span>';
    desc = 'The console only accepts connections from the ZimaOS box itself. Reach it through ' +
      'an SSH tunnel (<code>ssh -L 5901:127.0.0.1:5901 …</code>) or a reverse proxy on the host.';
  }

  const portNow = st.autoport
    ? `automatic${st.live_port ? ' — currently ' + st.live_port : ''}`
    : (st.port || '—');

  card.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">Reachability ${badge}</div>
      <div class="usb-desc">${desc}</div>
      <div class="usb-meta">listen: ${escapeHtml(st.listen || '(default)')} · port: ${escapeHtml(String(portNow))}</div>
    </div>
    <div class="usb-actions">
      <select class="vnc-listen" title="Which interfaces the console answers on">
        <option value="127.0.0.1"${!exposed ? ' selected' : ''}>Local only (127.0.0.1)</option>
        <option value="0.0.0.0"${exposed ? ' selected' : ''}>All interfaces (0.0.0.0)</option>
      </select>
      <input type="number" class="vnc-port" min="5900" max="65535" placeholder="auto"
             value="${st.autoport ? '' : (st.port || '')}" title="Fixed port, or empty for automatic">
      <button class="btn-primary" data-act="apply">Apply</button>
    </div>
  `;

  card.querySelector('[data-act="apply"]').addEventListener('click', async () => {
    const listen = card.querySelector('.vnc-listen').value;
    const portRaw = card.querySelector('.vnc-port').value.trim();
    const port = portRaw === '' ? 0 : parseInt(portRaw, 10);
    if (portRaw !== '' && (!Number.isInteger(port) || port < 5900 || port > 65535)) {
      setStatus('Port must be between 5900 and 65535, or empty for automatic', 'error');
      return;
    }
    // Say why before the request fails: exposing an unprotected console is
    // refused by the daemon, and "set a password first" is more use than the
    // error that would come back.
    if (listen === '0.0.0.0' && !st.protected && !st.pinned) {
      setStatus('Set a VNC password first — VM Extras will not put an unprotected console on the LAN', 'error');
      return;
    }
    if (listen === '0.0.0.0' && !exposed &&
        !confirm(`Open "${vm}"'s console to every interface again?\n\n` +
                 `It will be reachable from the whole LAN — with its password, but reachable.`)) return;
    try {
      const res = await api(`/vnc/${encodeURIComponent(vm)}`, {
        method: 'POST',
        body: JSON.stringify({ listen, port }),
      });
      setStatus(res.note ? `Reachability updated — ${res.note}` : 'Reachability updated', 'ok');
      renderVNCForCurrentVM();
    } catch (e) {
      setStatus(`Failed: ${e.message}`, 'error');
    }
  });

  return card;
}

function vncCard(vm, st) {
  const card = document.createElement('div');
  card.className = 'usb-row card';

  // A VM with no VNC graphics device — nothing to protect.
  if (!st.present) {
    card.innerHTML = `
      <div class="usb-info">
        <div class="usb-id">No VNC console</div>
        <div class="usb-desc">This VM has no VNC graphics device — nothing to secure here.</div>
      </div>`;
    return card;
  }

  // Three states, not two. The password is written to the persistent config,
  // which the running qemu never re-reads — so between setting it and the next
  // VM start the config says "protected" while the live console still lets
  // anyone in. Showing that as a plain green "password set" would be a green
  // badge over an open door.
  let badge, desc;
  if (st.live_error) {
    badge = '<span class="badge badge-danger">live state unknown</span>';
    desc = 'Could not read the running console\'s state: ' + escapeHtml(st.live_error);
  } else if (st.restart_required) {
    badge = '<span class="badge badge-warn">restart required</span>';
    desc = 'The password is saved in the VM config, but the console that is ' +
      'running right now still started without one and remains open to the ' +
      'LAN. Restart the VM to close it.';
  } else if (st.protected) {
    badge = '<span class="badge badge-current">password set</span>';
    desc = 'A VNC password is set. VM Extras re-applies it if the ZVM UI strips it.';
  } else {
    badge = '<span class="badge badge-danger">exposed — no password</span>';
    desc = 'Anyone on the LAN can open this VM\'s console without a password.';
  }

  card.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">VNC console ${badge}</div>
      <div class="usb-desc">${desc}</div>
      <div class="usb-meta">listen address: ${escapeHtml(st.listen || '(default)')}${st.pinned ? ' · pinned by VM Extras' : ''}</div>
    </div>
    <div class="usb-actions">
      <input type="password" class="vnc-pw" placeholder="1–8 characters" maxlength="8" autocomplete="new-password">
      <button class="btn-primary" data-act="set">${st.protected ? 'Change password' : 'Set password'}</button>
      ${st.protected ? '<button class="btn-danger" data-act="clear">Remove</button>' : ''}
    </div>
  `;

  card.querySelector('[data-act="set"]').addEventListener('click', async () => {
    const pw = card.querySelector('.vnc-pw').value;
    if (!pw) { setStatus('Enter a password', 'error'); return; }
    if (pw.length > 8) { setStatus('VNC passwords are limited to 8 characters', 'error'); return; }
    try {
      const res = await api(`/vnc/${encodeURIComponent(vm)}`, {
        method: 'POST',
        body: JSON.stringify({ password: pw }),
      });
      setStatus(res.note ? `VNC password set — ${res.note}` : 'VNC password set', 'ok');
      renderVNCForCurrentVM();
    } catch (e) {
      setStatus(`Failed: ${e.message}`, 'error');
    }
  });

  const clearBtn = card.querySelector('[data-act="clear"]');
  if (clearBtn) clearBtn.addEventListener('click', async () => {
    if (!confirm(`Remove the VNC password from "${vm}"?\n\nThe console will be open on the LAN again with no authentication.`)) return;
    try {
      await api(`/vnc/${encodeURIComponent(vm)}`, { method: 'DELETE' });
      setStatus('VNC password removed', 'ok');
      renderVNCForCurrentVM();
    } catch (e) {
      setStatus(`Failed: ${e.message}`, 'error');
    }
  });

  return card;
}

// ---------- METRICS ----------

let metricsTimer = null;
let metricsPrev = null;

function stopMetrics() {
  if (metricsTimer) { clearInterval(metricsTimer); metricsTimer = null; }
}

async function renderMetricsTab() {
  await loadVMs();
  metricsPrev = null;
  metricsHistory = {};
  const sel = $('#metrics-vm-select');
  if (!sel.value && vmCache.length) sel.value = vmCache[0].name;
  await pollMetrics();
  stopMetrics();
  metricsTimer = setInterval(pollMetrics, 2000);
}

async function pollMetrics() {
  const vm = $('#metrics-vm-select').value;
  const body = $('#metrics-body');
  if (!vm) { body.innerHTML = '<div class="empty">No VM selected.</div>'; return; }
  try {
    renderMetrics(await api(`/metrics/${encodeURIComponent(vm)}`));
  } catch (e) {
    body.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function metricRate(cur, prev, dtSec) {
  if (prev == null || dtSec <= 0 || cur < prev) return null;
  return (cur - prev) / dtSec;
}

// fmtRate renders a bytes/second value; 0 stays "0 B/s" (fmtBytes maps 0 → "?").
function fmtRate(r) {
  if (r == null) return '…';
  if (r === 0) return '0 B/s';
  return fmtBytes(r) + '/s';
}

function metricCard(label, value, sub, series, sparkOpts) {
  return `<div class="metric-card">
    <div class="metric-lbl">${label}</div>
    <div class="metric-num">${value}</div>
    <div class="metric-sub">${sub || '&nbsp;'}</div>
    ${series ? sparkline(series, sparkOpts) : ''}
  </div>`;
}

// ---------- METRIC HISTORY ----------

// Samples are kept in the page only: HISTORY_LEN points at the 2 s poll
// interval is a bit over three minutes of scrollback, which is what the
// sparklines draw. Switching VMs or leaving the tab discards them — there is
// no server-side time series behind this.
const HISTORY_LEN = 100;
let metricsHistory = {}; // key -> array of numbers (nulls allowed for gaps)

function pushHistory(key, value) {
  if (!metricsHistory[key]) metricsHistory[key] = [];
  const a = metricsHistory[key];
  a.push(value);
  if (a.length > HISTORY_LEN) a.shift();
  return a;
}

// sparkline renders a series as an inline SVG polyline, scaled to its own
// max so a quiet interface still shows its shape. opts.max pins the top of
// the scale (used for percentages, where 100 is the meaningful ceiling).
function sparkline(series, opts = {}) {
  const pts = series.filter(v => v != null && isFinite(v));
  if (pts.length < 2) return '<div class="spark spark-empty"></div>';
  const w = 100, h = 24;
  const max = opts.max != null ? opts.max : Math.max(...pts, 0.0001);
  const scale = max > 0 ? max : 1;
  const step = w / (HISTORY_LEN - 1);
  // Right-align: the newest sample sits at the right edge.
  const offset = w - (series.length - 1) * step;
  let d = '';
  series.forEach((v, i) => {
    if (v == null || !isFinite(v)) return;
    const x = (offset + i * step).toFixed(2);
    const y = (h - Math.min(v / scale, 1) * (h - 2) - 1).toFixed(2);
    d += (d ? ' L' : 'M') + x + ',' + y;
  });
  if (!d) return '<div class="spark spark-empty"></div>';
  return `<svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" aria-hidden="true">
    <path d="${d}" fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke"/>
  </svg>`;
}

// memoryCard distinguishes two very different numbers that v0.6.3 conflated.
//
// balloon.current / balloon.maximum is what the *host* has handed the VM. With
// an uninflated balloon those are equal, so every VM rendered as "100 %" —
// the bug a user reported against v0.6.3. Actual consumption has to come from
// inside the guest (balloon.available minus balloon.unused), and that is only
// there when the guest runs a virtio-balloon driver and libvirt is collecting
// its statistics. When it isn't, the card says "allocated" and shows no
// percentage at all rather than inventing one.
function memoryCard(m) {
  if (m.mem_stats) {
    // Two different true answers, so both are shown:
    //   used  = available - unused  → pages not free, page cache included
    //   avail = usable              → the guest's own MemAvailable, i.e. what
    //                                 applications can still take without swapping
    // They are deliberately not combined into a second "used" figure. Linux
    // subtracts the low watermark from MemAvailable, so MemAvailable can sit
    // *below* MemFree on an idle guest (measured on this host: 3.47 GiB usable
    // vs 3.59 GiB unused of 3.63 GiB) — "available - usable" would then exceed
    // "available - unused" and read like a contradiction.
    const usedKiB = m.mem_avail_kib - m.mem_unused_kib;
    const pct = m.mem_avail_kib ? (usedKiB / m.mem_avail_kib * 100) : null;
    const parts = [`${fmtBytes(usedKiB * 1024)} used`];
    if (m.mem_usable_kib) parts.push(`${fmtBytes(m.mem_usable_kib * 1024)} avail. to apps`);
    parts.push(`${fmtBytes(m.mem_avail_kib * 1024)} total`);
    return metricCard('Memory',
      pct == null ? fmtBytes(usedKiB * 1024) : pct.toFixed(0) + ' %',
      parts.join(' · '),
      pushHistory('mem', pct), { max: 100 });
  }
  pushHistory('mem', null);
  const sub = m.state === 'running'
    ? 'in-guest usage unavailable — no balloon stats yet'
    : 'in-guest usage needs a running VM';
  return metricCard('Memory (allocated)', fmtBytes(m.mem_cur_kib * 1024), sub);
}

function renderMetrics(m) {
  const body = $('#metrics-body');
  const prev = (metricsPrev && metricsPrev.vm === m.vm) ? metricsPrev : null;
  const dt = prev ? (m.ts_ms - prev.ts_ms) / 1000 : 0;

  let cpu = null;
  if (prev && dt > 0 && m.cpu_time_ns >= prev.cpu_time_ns) {
    cpu = ((m.cpu_time_ns - prev.cpu_time_ns) / (dt * 1e9)) / (m.vcpus || 1) * 100;
  }

  const cards = [
    metricCard('State', escapeHtml(m.state), ''),
    metricCard('CPU', cpu == null ? '…' : cpu.toFixed(1) + ' %', `${m.vcpus || '?'} vCPU`,
      pushHistory('cpu', cpu)),
    memoryCard(m),
  ];
  for (const n of (m.nets || [])) {
    const p = prev && prev.nets ? prev.nets.find(x => x.name === n.name) : null;
    const rx = metricRate(n.rx_bytes, p ? p.rx_bytes : null, dt);
    const tx = metricRate(n.tx_bytes, p ? p.tx_bytes : null, dt);
    cards.push(metricCard(`Net · ${escapeHtml(n.name)}`,
      `&darr; ${fmtRate(rx)}`, `&uarr; ${fmtRate(tx)}`,
      pushHistory('net:' + n.name, rx)));
  }
  for (const b of (m.blocks || [])) {
    const p = prev && prev.blocks ? prev.blocks.find(x => x.name === b.name) : null;
    const rd = metricRate(b.rd_bytes, p ? p.rd_bytes : null, dt);
    const wr = metricRate(b.wr_bytes, p ? p.wr_bytes : null, dt);
    cards.push(metricCard(`Disk · ${escapeHtml(b.name)}`,
      `R ${fmtRate(rd)}`, `W ${fmtRate(wr)}`,
      pushHistory('blk:' + b.name, rd)));
  }
  body.innerHTML = `<div class="metric-grid">${cards.join('')}</div>`;
  metricsPrev = m;
}

// ---------- NETWORK ----------

let netNetworks = [];

async function renderNetworkTab() {
  await loadVMs();
  try {
    const r = await api('/net/networks');
    netNetworks = r.data || [];
  } catch (e) { netNetworks = []; }
  await renderNICsForCurrentVM();
}

async function renderNICsForCurrentVM() {
  const list = $('#net-list');
  const vm = $('#net-vm-select').value;
  if (!vm) { list.innerHTML = '<div class="empty">No VM selected.</div>'; return; }
  list.innerHTML = '<div class="loading">Loading…</div>';
  try {
    const r = await api(`/net/${encodeURIComponent(vm)}`);
    const nics = r.data || [];
    if (nics.length === 0) {
      list.innerHTML = '<div class="empty">This VM has no network interfaces.</div>';
      return;
    }
    list.innerHTML = '';
    for (const n of nics) list.appendChild(nicRow(vm, n));
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function nicRow(vm, nic) {
  const row = document.createElement('div');
  row.className = 'usb-row card';
  const netOpts = netNetworks.map(n =>
    `<option value="${escapeHtml(n.name)}"${n.name === nic.source ? ' selected' : ''}>${escapeHtml(n.name)}${n.active ? '' : ' (inactive)'}</option>`
  ).join('') || '<option value="">no libvirt networks</option>';
  const modelOpts = ['virtio', 'e1000', 'e1000e', 'rtl8139'].map(m =>
    `<option value="${m}"${m === nic.model ? ' selected' : ''}>${m}</option>`
  ).join('');
  row.innerHTML = `
    <div class="usb-info">
      <div class="usb-id">${escapeHtml(nic.mac || '(no mac)')}</div>
      <div class="usb-meta">currently: ${escapeHtml(nic.type)} · ${escapeHtml(nic.source || '–')} · ${escapeHtml(nic.model || '–')}</div>
    </div>
    <div class="usb-actions">
      <select class="nic-net" title="Target libvirt network">${netOpts}</select>
      <select class="nic-model" title="NIC model">${modelOpts}</select>
      <button class="btn-primary" data-act="apply">Apply</button>
    </div>
  `;
  row.querySelector('[data-act="apply"]').addEventListener('click', async () => {
    const network = row.querySelector('.nic-net').value;
    const model = row.querySelector('.nic-model').value;
    if (!network) { setStatus('No network selected', 'error'); return; }
    if (!confirm(`Switch NIC ${nic.mac} of "${vm}" to network "${network}" (model ${model})?\n\nTakes effect on the next VM start.`)) return;
    try {
      const res = await api(`/net/${encodeURIComponent(vm)}/${encodeURIComponent(nic.mac)}`, {
        method: 'PUT',
        body: JSON.stringify({ network, model }),
      });
      setStatus(res.note ? `NIC updated — ${res.note}` : 'NIC updated', 'ok');
      renderNICsForCurrentVM();
    } catch (e) {
      setStatus(`Update failed: ${e.message}`, 'error');
    }
  });
  return row;
}

// ---------- BACKUP ----------

let backupTimer = null;

function stopBackupPoll() {
  if (backupTimer) { clearInterval(backupTimer); backupTimer = null; }
}

async function renderBackupTab() {
  await loadVMs();
  await loadBackupDest();
  await renderBackupJobs();
}

// The target directory is remembered on the appliance, not in this browser —
// v0.6.3 reset the field to the default on every visit, so anyone backing up
// to a remote mount had to retype the path each time. The daemon stores the
// path that last started a job; an empty field still means "use the default".
async function loadBackupDest() {
  const input = $('#backup-dest');
  if (!input) return;
  try {
    const s = await api('/settings');
    if (s && s.backup_dir) {
      input.value = s.backup_dir;
      input.title = 'Last used target — edit to change it';
    }
  } catch (e) { /* a missing preference just leaves the placeholder */ }
}

async function renderBackupJobs() {
  const list = $('#backup-list');
  try {
    const r = await api('/backup');
    const jobs = r.data || [];
    if (jobs.length === 0) {
      list.innerHTML = '<div class="empty">No backups yet.</div>';
    } else {
      list.innerHTML = '';
      for (const j of jobs) list.appendChild(backupRow(j));
    }
    // Auto-refresh while a job is still running.
    if (jobs.some(j => j.state === 'running')) {
      if (!backupTimer) backupTimer = setInterval(renderBackupJobs, 3000);
    } else {
      stopBackupPoll();
    }
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

function backupRow(j) {
  const row = document.createElement('div');
  row.className = 'snap-row card';
  const badge = j.state === 'done'
    ? '<span class="badge badge-current">done</span>'
    : j.state === 'failed'
      ? '<span class="badge" style="color:var(--danger)">failed</span>'
      : '<span class="badge">running</span>';
  const when = j.started_unix ? new Date(j.started_unix * 1000).toLocaleString() : '';
  const err = j.error
    ? `<div class="snap-meta" style="color:var(--danger)">${escapeHtml(j.error)}</div>` : '';
  row.innerHTML = `
    <div class="snap-info">
      <div class="snap-name">${escapeHtml(j.vm)} ${badge}</div>
      <div class="snap-meta">${escapeHtml(j.step || '')} · started ${escapeHtml(when)}</div>
      <div class="snap-meta"><code>${escapeHtml(j.dest)}</code></div>
      ${err}
    </div>
  `;
  return row;
}

// ---------- REMOTE MOUNTS ----------

const mountFormDefault = {
  id: '', name: '', type: 'nfs4', host: '', share: '',
  mountpoint: '', username: '', password: '',
  read_only: false, auto_mount: true, options: '',
};

function mountFormGet() {
  return {
    id: $('#mount-save-btn').dataset.editId || '',
    name: $('#mount-name').value.trim(),
    type: $('#mount-type').value,
    host: $('#mount-host').value.trim(),
    share: $('#mount-share').value.trim(),
    mountpoint: $('#mount-mp').value.trim(),
    username: $('#mount-user').value,
    password: $('#mount-pass').value,
    read_only: $('#mount-ro').checked,
    auto_mount: $('#mount-auto').checked,
    options: $('#mount-opts').value.trim(),
  };
}

function mountFormSet(e) {
  $('#mount-save-btn').dataset.editId = e.id || '';
  $('#mount-name').value = e.name || '';
  $('#mount-type').value = e.type || 'nfs4';
  $('#mount-host').value = e.host || '';
  $('#mount-share').value = e.share || '';
  $('#mount-mp').value = e.mountpoint || '';
  $('#mount-user').value = e.username || '';
  $('#mount-pass').value = ''; // never echo password back
  $('#mount-ro').checked = !!e.read_only;
  $('#mount-auto').checked = e.auto_mount !== false;
  $('#mount-opts').value = e.options || '';
  $('#mount-save-btn').textContent = e.id ? 'Update' : 'Save & mount';
  updateMountSMBVisibility();
}

function updateMountSMBVisibility() {
  const type = $('#mount-type').value;
  $('#mount-smb-row').style.display = (type === 'cifs' || type === 'smb') ? '' : 'none';
}

function mountRow(m) {
  const row = document.createElement('div');
  row.className = 'snap-row card';
  const target = m.type === 'cifs' || m.type === 'smb'
    ? `//${m.host}/${m.share}`
    : `${m.host}:${m.share}`;
  const status = m.mounted
    ? '<span class="badge badge-current">mounted</span>'
    : '<span class="badge">unmounted</span>';
  const err = m.last_error
    ? `<div class="snap-meta" style="color:var(--danger)">last error: ${escapeHtml(m.last_error)}</div>`
    : '';
  row.innerHTML = `
    <div class="snap-info">
      <div class="snap-name">${escapeHtml(m.name)} ${status}</div>
      <div class="snap-meta">${escapeHtml(m.type)} · ${escapeHtml(target)} → <code>${escapeHtml(m.mountpoint)}</code></div>
      <div class="snap-meta">${m.read_only ? 'read-only' : 'read-write'}${m.auto_mount ? ' · auto-mount' : ''}${m.username ? ' · user: ' + escapeHtml(m.username) : ''}</div>
      ${err}
    </div>
    <div class="snap-actions">
      ${m.mounted
        ? `<button class="btn-secondary" data-act="unmount">Unmount</button>`
        : `<button class="btn-primary" data-act="mount">Mount</button>`}
      <button class="btn-secondary" data-act="edit">Edit</button>
      <button class="btn-danger" data-act="delete">Delete</button>
    </div>
  `;
  row.querySelector('[data-act="mount"]')?.addEventListener('click', async () => {
    try { await api(`/mounts/${m.id}/mount`, { method: 'POST' }); setStatus(`Mounted: ${m.name}`, 'ok'); renderStorageTab(); }
    catch (e) { setStatus(`Mount failed: ${e.message}`, 'error'); renderStorageTab(); }
  });
  row.querySelector('[data-act="unmount"]')?.addEventListener('click', async () => {
    try { await api(`/mounts/${m.id}/unmount`, { method: 'POST' }); setStatus(`Unmounted: ${m.name}`, 'ok'); renderStorageTab(); }
    catch (e) { setStatus(`Unmount failed: ${e.message}`, 'error'); }
  });
  row.querySelector('[data-act="edit"]').addEventListener('click', () => mountFormSet(m));
  row.querySelector('[data-act="delete"]').addEventListener('click', async () => {
    if (!confirm(`Delete mount "${m.name}"? Will unmount first if mounted.`)) return;
    try { await api(`/mounts/${m.id}`, { method: 'DELETE' }); setStatus(`Deleted: ${m.name}`, 'ok'); renderStorageTab(); }
    catch (e) { setStatus(`Delete failed: ${e.message}`, 'error'); }
  });
  return row;
}

async function renderStorageTab() {
  const list = $('#mount-list');
  list.innerHTML = '<div class="loading">Loading mounts…</div>';
  try {
    const r = await api('/mounts');
    list.innerHTML = '';
    if (!r.data || r.data.length === 0) {
      list.innerHTML = '<div class="empty">No remote mounts configured yet.</div>';
      return;
    }
    for (const m of r.data) list.appendChild(mountRow(m));
  } catch (e) {
    list.innerHTML = `<div class="empty">Error: ${escapeHtml(e.message)}</div>`;
  }
}

// ---------- Tabs ----------

function switchTab(name) {
  stopMetrics();     // never leave a background poller running on another tab
  stopBackupPoll();
  $$('.tab-btn').forEach(b => {
    const active = b.dataset.tab === name;
    b.classList.toggle('active', active);
    b.setAttribute('aria-selected', active ? 'true' : 'false');
  });
  $$('.tab-panel').forEach(p => p.classList.toggle('active', p.id === 'tab-' + name));
  if (name === 'autostart') renderAutostartTab();
  else if (name === 'snapshots') renderSnapshotsTab();
  else if (name === 'usb') renderUSBTab();
  else if (name === 'pci') renderPCITab();
  else if (name === 'vnc') renderVNCTab();
  else if (name === 'tpm') renderTPMTab();
  else if (name === 'metrics') renderMetricsTab();
  else if (name === 'backup') renderBackupTab();
  else if (name === 'network') renderNetworkTab();
  else if (name === 'storage') renderStorageTab();
}

document.addEventListener('DOMContentLoaded', () => {
  $$('.tab-btn').forEach(b => b.addEventListener('click', () => switchTab(b.dataset.tab)));
  $('#refresh-btn').addEventListener('click', () => {
    const active = $('.tab-btn.active').dataset.tab;
    switchTab(active);
  });
  $('#snap-vm-select').addEventListener('change', renderSnapshotsForCurrentVM);
  $('#usb-vm-select').addEventListener('change', renderUSBTab);
  $('#pci-vm-select').addEventListener('change', renderPCITab);
  // A pure view preference, so it belongs in the browser rather than in the
  // appliance's settings store.
  const pciShowAll = $('#pci-show-all');
  try { pciShowAll.checked = localStorage.getItem('zvmx.pci.showAll') === '1'; } catch (e) { /* private mode */ }
  pciShowAll.addEventListener('change', () => {
    try { localStorage.setItem('zvmx.pci.showAll', pciShowAll.checked ? '1' : '0'); } catch (e) { /* ignore */ }
    renderPCITab();
  });
  $('#vnc-vm-select').addEventListener('change', renderVNCForCurrentVM);
  $('#tpm-vm-select').addEventListener('change', renderTPMForCurrentVM);
  // History belongs to one VM — carrying it across a switch would draw the
  // previous VM's curve under the new one's numbers.
  $('#metrics-vm-select').addEventListener('change', () => {
    metricsPrev = null;
    metricsHistory = {};
    pollMetrics();
  });
  $('#net-vm-select').addEventListener('change', renderNICsForCurrentVM);
  $('#snap-external').addEventListener('change', updateExtDirVisibility);
  $('#snap-storage-select').addEventListener('change', applyStorageSelection);
  $('#snap-vm-select').addEventListener('change', () => {
    // When the VM changes, refresh both snapshot list and storage path target.
    applyStorageSelection();
  });
  $('#snap-create-btn').addEventListener('click', async () => {
    const vm = $('#snap-vm-select').value;
    const name = $('#snap-name').value.trim();
    const desc = $('#snap-desc').value.trim();
    const external = $('#snap-external').checked;
    const externalDir = $('#snap-extdir').value.trim();
    if (!vm) { setStatus('No VM selected', 'error'); return; }
    if (!name) { setStatus('Snapshot name missing', 'error'); return; }
    // Mirrors validSnapshotName() on the server. Single spaces are fine —
    // they were already accepted here in v0.6.3, and the resulting snapshots
    // could then never be deleted. Doubled spaces are not: `virsh
    // snapshot-list` splits its columns on exactly that.
    if (!/^[A-Za-z0-9_.+][A-Za-z0-9_.+ -]*$/.test(name) || /\s{2}/.test(name) || name.includes('..')) {
      setStatus('Snapshot name may contain letters, digits, single spaces and _ . - + '
        + '— no leading -, no .., no double spaces', 'error');
      return;
    }
    if (external && externalDir && !externalDir.startsWith('/')) {
      setStatus('Target directory must be absolute (start with /)', 'error'); return;
    }
    try {
      $('#snap-create-btn').disabled = true;
      await api(`/snapshot/${encodeURIComponent(vm)}`, {
        method: 'POST',
        body: JSON.stringify({ name, description: desc, external, external_dir: externalDir }),
      });
      setStatus(`Snapshot created: ${name}`, 'ok');
      $('#snap-name').value = '';
      $('#snap-desc').value = '';
      await renderSnapshotsForCurrentVM();
    } catch (e) {
      setStatus(`Create failed: ${e.message}`, 'error');
    } finally {
      $('#snap-create-btn').disabled = false;
    }
  });

  // Snapshot schedule
  $('#sched-save-btn').addEventListener('click', async () => {
    const vm = $('#snap-vm-select').value;
    if (!vm) { setStatus('No VM selected', 'error'); return; }
    try {
      await api(`/schedule/${encodeURIComponent(vm)}`, {
        method: 'PUT',
        body: JSON.stringify({
          enabled: $('#sched-enabled').checked,
          interval_hours: parseInt($('#sched-interval').value, 10) || 24,
          keep: parseInt($('#sched-keep').value, 10) || 0,
        }),
      });
      setStatus('Schedule saved', 'ok');
      loadScheduleForCurrentVM();
    } catch (e) {
      setStatus(`Save failed: ${e.message}`, 'error');
    }
  });

  // Backup tab
  $('#backup-start-btn').addEventListener('click', async () => {
    const vm = $('#backup-vm-select').value;
    if (!vm) { setStatus('No VM selected', 'error'); return; }
    try {
      $('#backup-start-btn').disabled = true;
      await api(`/backup/${encodeURIComponent(vm)}`, {
        method: 'POST',
        body: JSON.stringify({ dest_dir: $('#backup-dest').value.trim() }),
      });
      setStatus('Backup started', 'ok');
      renderBackupJobs();
    } catch (e) {
      setStatus(`Backup failed: ${e.message}`, 'error');
    } finally {
      $('#backup-start-btn').disabled = false;
    }
  });

  // Storage tab wiring
  $('#mount-type').addEventListener('change', updateMountSMBVisibility);
  $('#mount-reset-btn').addEventListener('click', () => mountFormSet({ ...mountFormDefault }));
  $('#mount-save-btn').addEventListener('click', async () => {
    const e = mountFormGet();
    if (!e.name) { setStatus('Name required', 'error'); return; }
    if (!e.host) { setStatus('Host required', 'error'); return; }
    if (!e.share) { setStatus('Share required', 'error'); return; }
    if (!e.mountpoint) {
      // suggest a default
      const safeName = e.name.toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '');
      e.mountpoint = `/DATA/.zvmx-mounts/${safeName || 'share'}`;
      $('#mount-mp').value = e.mountpoint;
    }
    try {
      $('#mount-save-btn').disabled = true;
      const method = e.id ? 'PUT' : 'POST';
      const url = e.id ? `/mounts/${e.id}` : '/mounts';
      const r = await api(url, { method, body: JSON.stringify(e) });
      if (r.mount_error) {
        setStatus(`Saved, but mount failed: ${r.mount_error}`, 'error');
      } else {
        setStatus(`Saved: ${e.name}`, 'ok');
      }
      mountFormSet({ ...mountFormDefault });
      renderStorageTab();
    } catch (err) {
      setStatus(`Save failed: ${err.message}`, 'error');
    } finally {
      $('#mount-save-btn').disabled = false;
    }
  });

  loadVersion();
  renderAutostartTab();
});
