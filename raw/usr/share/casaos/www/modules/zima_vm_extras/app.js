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

  ctrl.append(toggleWrap, wdField, orderField, delayField);
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
      if (!extDirInput.value) extDirInput.value = r.default_external_dir;
    }
    // Sync the storage dropdown to the prefix of the current path (if it matches a known mount).
    syncStorageSelectionFromPath();
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

function updateExtDirVisibility() {
  const row = $('#snap-extdir-row');
  if (!row) return;
  row.style.display = $('#snap-external').checked ? '' : 'none';
}

// When the user picks a storage target, rewrite extDirInput to that mount + /zima-vm-extras-snapshots/<vm>.
function applyStorageSelection() {
  const sel = $('#snap-storage-select');
  const ext = $('#snap-extdir');
  const vm = $('#snap-vm-select').value;
  if (!sel || !ext) return;
  if (sel.value === '__custom__') {
    ext.removeAttribute('readonly');
    ext.focus();
    return;
  }
  if (sel.value) {
    const base = sel.value.replace(/\/+$/, '');
    ext.value = `${base}/zima-vm-extras-snapshots/${vm || ''}`.replace(/\/+$/, '/');
    if (!vm) ext.value = `${base}/zima-vm-extras-snapshots/`;
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
    for (const dev of r.data) list.appendChild(pciRow(dev, pinned));
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
    desc = 'Secure Boot is enabled but no keys are enrolled, so the firmware ' +
      'is in setup mode and validates nothing. Windows 11 setup normally ' +
      'accepts this — it checks for Secure Boot capability — but the VM is ' +
      'not actually protected by it. ZimaOS ships no NVRAM template with keys ' +
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

function metricCard(label, value, sub) {
  return `<div class="metric-card">
    <div class="metric-lbl">${label}</div>
    <div class="metric-num">${value}</div>
    <div class="metric-sub">${sub || '&nbsp;'}</div>
  </div>`;
}

function renderMetrics(m) {
  const body = $('#metrics-body');
  const prev = (metricsPrev && metricsPrev.vm === m.vm) ? metricsPrev : null;
  const dt = prev ? (m.ts_ms - prev.ts_ms) / 1000 : 0;

  let cpu = null;
  if (prev && dt > 0 && m.cpu_time_ns >= prev.cpu_time_ns) {
    cpu = ((m.cpu_time_ns - prev.cpu_time_ns) / (dt * 1e9)) / (m.vcpus || 1) * 100;
  }
  const memPct = m.mem_max_kib ? (m.mem_cur_kib / m.mem_max_kib * 100) : null;

  const cards = [
    metricCard('State', escapeHtml(m.state), ''),
    metricCard('CPU', cpu == null ? '…' : cpu.toFixed(1) + ' %', `${m.vcpus || '?'} vCPU`),
    metricCard('Memory',
      memPct == null ? fmtBytes(m.mem_cur_kib * 1024) : memPct.toFixed(0) + ' %',
      `${fmtBytes(m.mem_cur_kib * 1024)} / ${fmtBytes(m.mem_max_kib * 1024)}`),
  ];
  for (const n of (m.nets || [])) {
    const p = prev && prev.nets ? prev.nets.find(x => x.name === n.name) : null;
    const rx = metricRate(n.rx_bytes, p ? p.rx_bytes : null, dt);
    const tx = metricRate(n.tx_bytes, p ? p.tx_bytes : null, dt);
    cards.push(metricCard(`Net · ${escapeHtml(n.name)}`,
      `&darr; ${fmtRate(rx)}`, `&uarr; ${fmtRate(tx)}`));
  }
  for (const b of (m.blocks || [])) {
    const p = prev && prev.blocks ? prev.blocks.find(x => x.name === b.name) : null;
    const rd = metricRate(b.rd_bytes, p ? p.rd_bytes : null, dt);
    const wr = metricRate(b.wr_bytes, p ? p.wr_bytes : null, dt);
    cards.push(metricCard(`Disk · ${escapeHtml(b.name)}`,
      `R ${fmtRate(rd)}`, `W ${fmtRate(wr)}`));
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
  await renderBackupJobs();
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
  $('#vnc-vm-select').addEventListener('change', renderVNCForCurrentVM);
  $('#tpm-vm-select').addEventListener('change', renderTPMForCurrentVM);
  $('#metrics-vm-select').addEventListener('change', () => { metricsPrev = null; pollMetrics(); });
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
    if (/[\/\\.]/.test(name)) { setStatus('Snapshot name must not contain /, \\ or .', 'error'); return; }
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
