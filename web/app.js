"use strict";
// Behind a reverse proxy the UI may live under a sub-path like /backups/.
// Every request is relative, so the page URL must end in a slash.
if (!location.pathname.endsWith("/") && !location.pathname.endsWith(".html")) {
  location.replace(location.pathname + "/" + location.search + location.hash);
}

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = v => String(v ?? "").replace(/[&<>"']/g, c => ({"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"}[c]));
let session = null, routeToken = 0, pendingTimer = null;

async function api(method, path, body) {
  const opts = {method, headers: {"X-PBCM": "1"}, credentials: "same-origin"};
  if (body instanceof Blob) { opts.headers["Content-Type"] = "application/octet-stream"; opts.body = body; }
  else if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
  let res;
  try { res = await fetch("api" + path, opts); }
  catch (_) { throw new Error("Can't reach the server. Check that it's running and your network connection."); }
  let data = {};
  try { data = await res.json(); } catch (_) {}
  if (res.status === 401 && !path.startsWith("/login") && !path.startsWith("/setup")) { showGate("login"); throw new Error(data.error || "Sign in to continue."); }
  if (!res.ok) throw new Error(data.error || `The server returned ${res.status}.`);
  return data;
}

function toast(msg, kind = "") {
  const el = document.createElement("div");
  el.className = "toast " + kind; el.textContent = msg;
  $("#toasts").append(el);
  setTimeout(() => el.remove(), kind === "bad" ? 7000 : 3500);
}
function showError(box, msg) { box.textContent = msg; box.classList.remove("hidden"); }
function fmtDate(ts) { return ts ? new Date(ts * 1000).toLocaleDateString([], {year: "numeric", month: "short", day: "numeric"}) : "—"; }
function plural(n, word) { return `${n} ${word}${n === 1 ? "" : "s"}`; }

/* ---------- sign-in gate: setup, password, two-step ---------- */
let loginTicket = null, recoveryMode = false;

function showGate(which) {
  $("#app").classList.add("hidden");
  $("#gate").classList.remove("hidden");
  for (const id of ["setup-form", "login-form", "totp-form"]) $("#" + id).classList.add("hidden");
  $$("#gate [data-error]").forEach(e => e.classList.add("hidden"));
  const name = session ? session.server_name : "";
  if (which === "setup") {
    $("#gate-title").textContent = "Set up PBC Manager";
    $("#gate-sub").textContent = name ? `Finish setting up the server on ${name}.` : "";
    $("#setup-form").classList.remove("hidden");
    setTimeout(() => $("#setup-form [name=code]").focus(), 0);
  } else {
    $("#gate-title").textContent = "PBC Manager";
    $("#gate-sub").textContent = name ? `Sign in to manage backups from ${name}.` : "";
    loginTicket = null;
    $("#login-form").classList.remove("hidden");
    setTimeout(() => $("#login-form [name=username]").focus(), 0);
  }
  drawGateBanner();
}

function drawGateBanner() {
  const p = session && session.network_pending;
  $("#gate-banner").innerHTML = p
    ? `<div class="banner info">New network settings are being tried. ${p.here ? "Sign in to keep them" : "They'll be undone"} within ${plural(p.expires_in, "second")} unless they're confirmed.</div>`
    : "";
}

function setRecoveryMode(on) {
  recoveryMode = on;
  const inp = $("#totp-form [name=code]");
  inp.value = "";
  inp.classList.toggle("recovery", on);
  inp.inputMode = on ? "text" : "numeric";
  inp.maxLength = on ? 11 : 7;
  inp.autocomplete = on ? "off" : "one-time-code";
  inp.placeholder = on ? "xxxxx-xxxxx" : "";
  $("#totp-label").textContent = on ? "Recovery code" : "Verification code";
  $("#totp-help").textContent = on ? "Enter one of the recovery codes you saved when you set up two-step verification. Each code works once."
    : "Open your authenticator app and enter the 6-digit code for PBC Manager.";
  $("#use-recovery").textContent = on ? "Use my authenticator app instead" : "Use a recovery code instead";
  $("#totp-form [data-error]").classList.add("hidden");
  inp.focus();
}

async function finishSignIn(r) {
  $("#login-form").password.value = "";
  $("#totp-form").code.value = "";
  loginTicket = null;
  await loadSession();
  showApp();
  if (r && r.recovery_used) toast(`Signed in with a recovery code. ${r.recovery_left} left.${r.recovery_left <= 3 ? " Create new ones from Account." : ""}`, r.recovery_left <= 3 ? "bad" : "");
}

function wireGate() {
  $("#setup-form").addEventListener("submit", async e => {
    e.preventDefault();
    const f = e.target, err = $("[data-error]", f);
    err.classList.add("hidden");
    if (f.password.value !== f.again.value) return showError(err, "The passwords don't match.");
    const btn = $("button[type=submit]", f); btn.disabled = true;
    try {
      await api("POST", "/setup", {code: f.code.value, username: f.username.value, password: f.password.value});
      f.reset();
      await finishSignIn();
      toast("Setup finished. Next, turn on two-step verification under Account.");
    } catch (ex) { showError(err, ex.message); }
    finally { btn.disabled = false; }
  });
  $("#login-form").addEventListener("submit", async e => {
    e.preventDefault();
    const f = e.target, err = $("[data-error]", f);
    err.classList.add("hidden");
    const btn = $("button[type=submit]", f); btn.disabled = true;
    try {
      const r = await api("POST", "/login", {username: f.username.value, password: f.password.value});
      if (r.totp_required) {
        loginTicket = r.ticket;
        f.classList.add("hidden");
        $("#totp-form").classList.remove("hidden");
        setRecoveryMode(false);
        return;
      }
      await finishSignIn(r);
    } catch (ex) { showError(err, ex.message); }
    finally { btn.disabled = false; }
  });
  $("#totp-form").addEventListener("submit", async e => {
    e.preventDefault();
    const f = e.target, err = $("[data-error]", f);
    err.classList.add("hidden");
    const btn = $("button[type=submit]", f); btn.disabled = true;
    try {
      await finishSignIn(await api("POST", "/login/totp", {ticket: loginTicket, code: f.code.value}));
    } catch (ex) {
      if (/timed out/i.test(ex.message)) { showGate("login"); showError($("#login-form [data-error]"), ex.message); }
      else { showError(err, ex.message); f.code.select(); }
    } finally { btn.disabled = false; }
  });
  $("#totp-form [name=code]").addEventListener("input", e => {
    if (!recoveryMode && /^\d{6}$/.test(e.target.value.replace(/\s/g, ""))) $("#totp-form").requestSubmit();
  });
  $("#use-recovery").addEventListener("click", () => setRecoveryMode(!recoveryMode));
  $("#totp-back").addEventListener("click", () => showGate("login"));
  $("#logout").addEventListener("click", async () => {
    await api("POST", "/logout").catch(() => {});
    session.user = null;
    showGate("login");
  });
}

/* ---------- app shell, routing and the pending-network banner ---------- */
async function loadSession() {
  session = await api("GET", "/session");
  return session;
}

function showApp() {
  $("#gate").classList.add("hidden");
  $("#app").classList.remove("hidden");
  $("#server-name").textContent = session.server_name;
  $("#version").textContent = `Version ${session.version}`;
  document.title = `${session.server_name} · PBC Manager`;
  drawPendingBanner();
  route();
}

function render(html) { $("#view").innerHTML = html; }

const routes = [
  [/^#\/(?:dashboard|overview)$/, "dashboard", viewDashboard],
  [/^#\/clients$/, "clients", viewClients],
  [/^#\/clients\/new$/, "clients", viewClientNew],
  [/^#\/clients\/(\w+)$/, "clients", viewClient],
  [/^#\/jobs$/, "jobs", viewJobs],
  [/^#\/jobs\/new$/, "jobs", t => viewJobForm(null, t)],
  [/^#\/jobs\/new\/(\w+)$/, "jobs", (client, t) => viewJobForm(null, t, client)],
  [/^#\/jobs\/(\w+)\/edit$/, "jobs", viewJobForm],
  [/^#\/jobs\/(\w+)$/, "jobs", viewJobDetail],
  [/^#\/destinations$/, "destinations", viewDestinations],
  [/^#\/destinations\/new$/, "destinations", t => viewDestinationForm(null, t)],
  [/^#\/destinations\/(\w+)$/, "destinations", viewDestinationForm],
  [/^#\/activity$/, "activity", viewActivity],
  [/^#\/alerts$/, "alerts", viewAlerts],
  [/^#\/activity\/(\w+)\/([\w-]+)$/, "activity", viewRun],
  [/^#\/settings$/, "settings", viewSettings],
  [/^#\/updates$/, "updates", viewUpdates],
  [/^#\/account$/, "account", viewAccount],
  [/^#\/confirm-network\/([\w-]+)$/, "settings", viewConfirmNetwork],
];

let pollTimer = null;
function stopPoll() { if (pollTimer) clearInterval(pollTimer); pollTimer = null; }
function poll(fn, ms) { stopPoll(); pollTimer = setInterval(() => fn().catch(() => {}), ms); }

async function route() {
  if (!session || !session.user) return;
  stopPoll();
  const token = ++routeToken;
  const hash = location.hash || "#/dashboard";
  const match = routes.find(([re]) => re.test(hash));
  if (!match) { location.hash = "#/dashboard"; return; }
  const [re, nav, fn] = match;
  $$(".nav a").forEach(a => a.dataset.nav === nav ? a.setAttribute("aria-current", "page") : a.removeAttribute("aria-current"));
  render(`<div class="loading">Loading…</div>`);
  try { await fn(...hash.match(re).slice(1), token); }
  catch (ex) { if (token === routeToken) render(`<div class="banner bad">${esc(ex.message)}</div>`); }
}
window.addEventListener("hashchange", route);

function drawPendingBanner() {
  const p = session && session.network_pending, box = $("#pending-banner");
  clearTimeout(pendingTimer);
  if (!p) { box.innerHTML = ""; return; }
  if (location.hash.startsWith("#/confirm-network/")) { box.innerHTML = ""; }
  else if (p.here) {
    box.innerHTML = `<div class="banner info"><b>You're using the new network settings.</b> Keep them? If you don't, the server goes back to the previous settings in ${plural(p.expires_in, "second")}.
      <div class="btnrow"><button class="btn primary small" data-keep>Keep these settings</button><button class="btn small" data-undo>Undo</button></div></div>`;
  } else {
    box.innerHTML = `<div class="banner info"><b>New network settings are being tried.</b> Open <a href="${esc(p.url)}#/confirm-network/${esc(p.token)}">${esc(p.url)}</a> to keep them. Otherwise they're undone in ${plural(p.expires_in, "second")}.
      <div class="btnrow"><button class="btn small" data-undo>Undo now</button></div></div>`;
  }
  wirePendingButtons(box, p);
  pendingTimer = setTimeout(async () => {
    try { await loadSession(); } catch (_) { return; }
    drawPendingBanner();
    if (!session.network_pending && location.hash === "#/settings") route();
  }, 3000);
}

function wirePendingButtons(box, p) {
  const keep = $("[data-keep]", box), undo = $("[data-undo]", box);
  if (keep) keep.onclick = async () => {
    keep.disabled = true;
    try { await api("POST", "/settings/network/confirm", {token: p.token}); toast("New network settings kept."); await loadSession(); drawPendingBanner(); route(); }
    catch (ex) { toast(ex.message, "bad"); keep.disabled = false; }
  };
  if (undo) undo.onclick = async () => {
    undo.disabled = true;
    try { await api("POST", "/settings/network/cancel"); toast("Network change undone."); await loadSession(); drawPendingBanner(); route(); }
    catch (ex) { toast(ex.message, "bad"); undo.disabled = false; }
  };
}

/* ---------- dashboard ---------- */
async function viewDashboard(token) {
  let [{clients}, acct] = await Promise.all([api("GET", "/clients"), api("GET", "/account")]);
  if (token !== routeToken) return;
  // Only nudge about two-step verification while it's off.
  const twoStep = acct.totp_enabled ? "" : `<div class="banner warn"><b>Two-step verification is off.</b> Anyone with your password can manage every client's backups. <a href="#/account">Turn it on</a></div>`;
  const [alertCfg, upd] = await Promise.all([api("GET", "/alerts/settings"), api("GET", "/update").catch(() => null)]);
  const updateNote = updateBanner(upd);
  const noAlerts = alertCfg.settings.enabled ? "" : `<div class="banner warn">Email alerts are off, so a failed or missed backup won't notify anyone. <a href="#/alerts">Set up alerts</a></div>`;
  if (!clients.length) {
    render(`<div class="health"><span class="dot"></span><h1>No clients yet</h1></div>${twoStep}
      <div class="panel empty"><h2>Add your first client</h2>
        <p>A client is a Linux machine whose folders you want to back up. The server connects to it over SSH once to set it up; after that the client backs up on its own.</p>
        <a class="btn primary" href="#/clients/new">Add a client</a></div>`);
    return;
  }
  const draw = async () => {
    const [{jobs}, {sizes}, {destinations}, fresh, {metrics}] = await Promise.all([api("GET", "/jobs"), api("GET", "/sizes"), api("GET", "/destinations"), api("GET", "/clients"), api("GET", "/metrics")]);
    if (token !== routeToken) return;
    clients = fresh.clients;
    const trouble = clients.filter(c => c.status !== "ready" && c.status !== "setting-up");
    const head = trouble.length ? (trouble.length === 1 ? `${trouble[0].name} needs attention` : `${trouble.length} clients need attention`)
      : clients.length === 1 ? "Your client is ready" : `All ${clients.length} clients are ready`;
    const failing = jobs.filter(j => lastFinished(j) && lastFinished(j).status === "failed");
    let cls = trouble.length || failing.length ? "bad" : "ok", title = head;
    if (failing.length) title = failing.length === 1 ? `${failing[0].name} on ${failing[0].client_name} failed its last run` : `${failing.length} jobs failed their last run`;
    else if (!trouble.length && jobs.length) title = jobs.length === 1 ? "Your backup job is healthy" : `All ${jobs.length} backup jobs are healthy`;
    render(`<div class="health ${cls}"><span class="dot"></span><h1>${esc(title)}</h1></div>${updateNote}${twoStep}${jobs.length ? noAlerts : ""}
      ${jobs.length ? sizeTiles(sizes, destinations, jobs, clients) + metricCards(metrics) : `<div class="panel empty"><h2>No backup jobs yet</h2><p>Your clients are ready. Add a destination for your Proxmox Backup Server, then create a job to choose folders and a schedule.</p><div class="btnrow"><a class="btn primary" href="#/jobs/new">Create a backup job</a><a class="btn" href="#/destinations/new">Add a destination</a></div></div>`}
      <div class="pagehead mt-16 m-0"><h2 class="m-0">Clients and their backups</h2>${clients.length > 1 ? `<div class="btnrow"><button class="btn small" data-groups="open">Expand all</button><button class="btn small" data-groups="close">Collapse all</button></div>` : ""}</div>
      <div class="cgroups">${clientGroups(clients, jobs, sizes)}</div>`);
    bindClientGroups(draw);
    bindRowLinks();
    bindRunButtons(draw);
    bindSizeButtons(draw);
    sizeMeters($("#view"));
    drawBars($("#view"));
  };
  await draw();
  poll(draw, 10000);
}

/* ---------- sizes ---------- */
// spaceClass colours a datastore that's filling up: amber from 80%, red from 90%.
const spaceClass = pct => pct >= 90 ? "bad" : pct >= 80 ? "warn" : "";
const spacePct = sp => sp && sp.total ? Math.round(sp.used / sp.total * 100) : null;
function spaceHtml(sp, name) {
  if (!sp) return `<div class="sub">Not checked yet.</div>`;
  if (sp.error) return `<div class="sub bad-text">${esc(sp.error)}</div>`;
  const pct = spacePct(sp);
  if (pct == null) return `<div class="sub">The datastore didn't report its size.</div>`;
  return `<div class="meter ${spaceClass(pct)}" data-pct="${pct}" role="img" aria-label="${esc(name)}: ${pct}% used"><i></i></div>
    <div class="sub spaceline"><span>${bytes(sp.used)} of ${bytes(sp.total)} used, ${bytes(sp.avail)} free</span><b class="${pct >= 90 ? "bad-text" : pct >= 80 ? "warn-text" : ""}">${pct}%</b></div>`;
}
function sizeTiles(sz, destinations, jobs, clients) {
  const folders = new Set(), onClients = new Set();
  jobs.forEach(j => j.shares.forEach(sh => { folders.add(j.client_id + ":" + sh.path); onClients.add(j.client_id); }));
  const measuring = sz.measuring ? `<span class="pill busy">Measuring</span>` : "";
  let main = `<div class="bignum">${sz.folder_total == null ? "—" : bytes(sz.folder_total)}</div>`;
  let sub = sz.folder_total == null
    ? (sz.measuring ? "Clients are measuring their folders. Large folders can take a while." : "Folders haven't been measured yet.")
    : `In ${plural(folders.size, "folder")} on ${plural(onClients.size, "client")}${sz.folder_pending ? `, ${plural(sz.folder_pending, "folder")} not measured yet` : ""}. Measured ${esc(ago(sz.folder_measured))}.`;
  const failed = sz.folder_failed ? `<div class="sub warn-text mt-8">${plural(sz.folder_failed, "folder")} couldn't be measured. The job pages say why.</div>` : "";
  const latest = sz.backup_total == null ? "" : `<div class="sub mt-8">Newest backups, as PBS counts them: <b>${bytes(sz.backup_total)}</b></div>`;
  const ready = clients.filter(c => c.status === "ready" && jobs.some(j => j.client_id === c.id)).map(c => c.id);
  const used = destinations.filter(d => d.used_by.length);
  return `<div class="tiles">
    <div class="panel"><div class="tilehead"><h2>Data protected</h2>${measuring}</div>${main}<div class="sub">${sub}</div>${failed}${latest}
      ${ready.length ? `<div class="btnrow mt-12"><button class="btn small" data-measure="${esc(ready.join(","))}">Measure again</button></div>` : ""}</div>
    <div class="panel"><div class="tilehead"><h2>Destination space</h2></div>
      ${used.length ? used.map(d => `<div class="destspace"><div class="destname"><a class="jobname" href="#/destinations/${esc(d.id)}">${esc(d.name)}</a></div>${spaceHtml(sz.destinations[d.id], d.name)}</div>`).join("") : `<p class="muted">No destinations in use.</p>`}
      ${used.length ? `<div class="btnrow mt-12"><button class="btn small" data-checkspace>Check now</button><span class="sub">${esc(checkedText(used.map(d => sz.destinations[d.id])))}</span></div>` : ""}</div>
  </div>`;
}
function checkedText(list) {
  const times = list.filter(Boolean).map(x => x.checked).filter(Boolean);
  return times.length ? "Checked " + ago(Math.min(...times)) : "";
}
function bindSizeButtons(refresh, job) {
  $$("[data-measure]").forEach(b => b.addEventListener("click", async () => {
    b.disabled = true;
    try {
      await Promise.all(b.dataset.measure.split(",").map(id => api("POST", `/clients/${id}/measure`, job ? {job} : {})));
      toast("Measuring in the background. Sizes update when it's done.");
      setTimeout(refresh, 4000);
    } catch (ex) { toast(ex.message, "bad"); b.disabled = false; }
  }));
  $$("[data-checkspace]").forEach(b => b.addEventListener("click", async () => {
    b.disabled = true;
    try { await api("POST", "/sizes/check", b.dataset.checkspace ? {destination: b.dataset.checkspace} : job ? {job} : {}); toast("Checking with PBS."); setTimeout(refresh, 4000); }
    catch (ex) { toast(ex.message, "bad"); b.disabled = false; }
  }));
}
// jobSize is the short size shown with a job: its folders, or failing that
// its newest backup.
function jobSize(js) {
  if (!js) return "";
  if (js.folder_bytes != null) return (js.folder_complete ? "" : "at least ") + bytes(js.folder_bytes);
  if (js.backup_bytes != null) return bytes(js.backup_bytes) + " last backup";
  return js.measuring ? "measuring…" : "";
}

/* ---------- metric cards (Dashboard) ---------- */
// secs formats a duration in seconds: 45s, 12m 30s, 3h 05m.
function secs(n) {
  if (n == null) return "—";
  const h = Math.floor(n / 3600), m = Math.floor((n % 3600) / 60), s = n % 60;
  return h ? `${h}h ${String(m).padStart(2, "0")}m` : m ? `${m}m ${s}s` : `${s}s`;
}
const pct = p => p == null ? "—" : (p >= 99.95 ? "100" : p.toFixed(p >= 10 ? 0 : 1)) + "%";
const rateClass = p => p == null ? "" : p >= 95 ? "ok-text" : p >= 80 ? "warn-text" : "bad-text";

function metricCards(m) {
  const j = m.jobs, w = m.week, mo = m.month;
  const finished = w.succeeded + w.failed;
  const jobsLine = [`${j.enabled} enabled`, j.disabled && `${j.disabled} disabled`, j.by_hand && `${j.by_hand} by hand only`].filter(Boolean).join(" · ");
  const max = Math.max(1, ...m.daily.map(d => d.succeeded + d.failed));
  const bars = m.daily.map(d => {
    const label = `${new Date(d.day + "T12:00:00").toLocaleDateString([], {weekday: "short", month: "short", day: "numeric"})}: ${d.succeeded} succeeded, ${d.failed} failed`;
    return `<span class="bar" title="${esc(label)}" aria-label="${esc(label)}" role="img">
      <i class="bar-ok" data-h="${(d.succeeded / max * 100).toFixed(1)}"></i><i class="bar-bad" data-h="${(d.failed / max * 100).toFixed(1)}"></i></span>`;
  }).join("");
  const partial = r => r.partial ? `<div class="sub mt-8">Run history doesn't go back that far, so this covers the last ${plural(m.history.runs, "run")} the server keeps.</div>` : "";
  const list = (rows, empty) => rows.length ? `<ol class="toplist">${rows.join("")}</ol>` : `<p class="muted m-0">${empty}</p>`;
  return `<div class="tiles metrics">
    <div class="panel"><div class="tilehead"><h2>Backup jobs</h2></div>
      <div class="bignum">${j.total}</div>
      <div class="sub">${esc(jobsLine)}</div>
      <div class="sub mt-8">On ${plural(j.clients, "client")}, backing up to ${plural(j.destinations, "destination")}.</div></div>
    <div class="panel"><div class="tilehead"><h2>Success rate</h2><span class="sub">last 7 days</span></div>
      <div class="bignum ${rateClass(w.percent)}">${pct(w.percent)}</div>
      <div class="sub">${finished ? `${w.succeeded} of ${plural(finished, "finished run")} succeeded${w.cancelled ? `, ${w.cancelled} cancelled` : ""}.` : "No backups have finished in the last 7 days."}</div>
      <div class="sub">Last 30 days: <b class="${rateClass(mo.percent)}">${pct(mo.percent)}</b>${mo.succeeded + mo.failed ? ` of ${plural(mo.succeeded + mo.failed, "run")}` : ""}</div>
      ${partial(mo)}
      <div class="bars mt-12" aria-label="Runs per day, last 14 days">${bars}</div>
      <div class="sub barlegend"><span><i class="key ok"></i>Succeeded</span><span><i class="key bad"></i>Failed</span><span class="muted">Last 14 days</span></div></div>
    <div class="panel"><div class="tilehead"><h2>Largest backups</h2></div>
      ${list(m.largest.map(x => `<li><a class="jobname" href="#/jobs/${esc(x.job_id)}">${esc(x.name)}</a> <span class="sub">${esc(x.client)}</span><b>${esc(bytes(x.bytes))}</b></li>`), "Sizes appear once folders are measured or the first backup is on PBS.")}
      <div class="sub mt-8">The newest backup as PBS counts it, or the folders' size before the first backup.</div></div>
    <div class="panel"><div class="tilehead"><h2>Longest running</h2><span class="sub">last 30 days</span></div>
      ${list(m.longest.map(x => `<li><a class="jobname" href="#/jobs/${esc(x.job_id)}">${esc(x.name)}</a> <span class="sub">${esc(x.client)}</span>
        <span class="toptime"><b>${esc(secs(x.avg_seconds))}</b> <span class="sub">average · <a href="#/activity/${esc(x.max_client_id)}/${esc(x.max_run_id)}">longest ${esc(secs(x.max_seconds))}</a></span></span></li>`), "Times appear after the first successful backup.")}
    </div>
  </div>`;
}
// drawBars sizes the daily bars (set from script: the CSP allows no inline styles).
function drawBars(root) { $$(".bars i[data-h]", root).forEach(i => { i.style.height = i.dataset.h + "%"; }); }

/* ---------- clients and their jobs (Dashboard) ---------- */
// Which client sections are open is remembered in this browser only.
const GROUPS_KEY = "pbcm.dashboard.open";
function groupState() { try { return JSON.parse(localStorage.getItem(GROUPS_KEY) || "{}"); } catch (_) { return {}; } }
function saveGroupState(s) { try { localStorage.setItem(GROUPS_KEY, JSON.stringify(s)); } catch (_) {} }

// clientSummary counts a client's jobs by state.
function clientSummary(c, jobs) {
  const mine = jobs.filter(j => j.client_id === c.id);
  const failing = mine.filter(j => { const l = lastFinished(j); return l && l.status === "failed"; }).length;
  return {jobs: mine, failing, running: mine.filter(isRunning).length, disabled: mine.filter(j => !j.enabled).length,
    trouble: failing > 0 || (c.status !== "ready" && c.status !== "setting-up")};
}

function clientGroups(clients, jobs, sizes) {
  const saved = groupState();
  return [...clients].sort((a, b) => a.name.localeCompare(b.name)).map(c => {
    const s = clientSummary(c, jobs);
    // Problems are never hidden in a closed section.
    const open = s.trouble || clients.length === 1 || saved[c.id] === true;
    const counts = [plural(s.jobs.length, "job"), s.running && `${s.running} running`, s.failing && `${s.failing} failing`, s.disabled && `${s.disabled} disabled`].filter(Boolean);
    const id = "cg-" + c.id;
    return `<section class="cgroup ${s.trouble ? "trouble" : ""}">
      <div class="cghead">
        <button type="button" class="cgtoggle" aria-expanded="${open}" aria-controls="${esc(id)}" data-group="${esc(c.id)}" ${s.trouble || clients.length === 1 ? 'data-pinned="1"' : ""}>
          <span class="chev" aria-hidden="true"></span>
          <span class="cgname"><b>${esc(c.name)}</b><span class="sub mono">${esc(c.address)}${c.port === 22 ? "" : ":" + esc(c.port)}</span></span>
          ${clientPill(c.status)}
          <span class="cgcounts">${counts.map(x => `<span class="${/failing/.test(x) ? "bad-text" : /running/.test(x) ? "busy-text" : ""}">${esc(x)}</span>`).join(" · ")}</span>
          <span class="cgmeta sub">${esc(ago(c.last_contact))}${c.client_version ? ` · client ${esc(c.client_version)}` : ""}</span>
        </button>
        <a class="btn small" href="#/clients/${esc(c.id)}">Client details</a>
      </div>
      <div class="cgbody" id="${esc(id)}" ${open ? "" : "hidden"}>
        ${s.jobs.length ? jobLedger(s.jobs, sizes, {noClient: true})
          : `<div class="cgempty"><span class="muted">No backup jobs yet.</span> <a href="#/jobs/new/${esc(c.id)}">Create a backup job</a></div>`}
      </div>
    </section>`;
  }).join("");
}

function bindClientGroups(redraw) {
  const setOpen = (btn, open) => {
    btn.setAttribute("aria-expanded", String(open));
    $("#" + btn.getAttribute("aria-controls")).hidden = !open;
  };
  $$("[data-group]").forEach(btn => btn.addEventListener("click", () => {
    const open = btn.getAttribute("aria-expanded") !== "true";
    setOpen(btn, open);
    const s = groupState(); s[btn.dataset.group] = open; saveGroupState(s);
  }));
  $$("[data-groups]").forEach(b => b.addEventListener("click", () => {
    const open = b.dataset.groups === "open", s = groupState();
    $$("[data-group]").forEach(btn => { if (!open && btn.dataset.pinned) return; setOpen(btn, open); s[btn.dataset.group] = open; });
    saveGroupState(s);
  }));
}

/* ---------- jobs ---------- */
const STATUS_WORD = {success: "Succeeded", failed: "Failed", running: "Running", cancelled: "Cancelled"};
const runPill = st => `<span class="pill ${{success: "ok", failed: "bad", running: "busy", cancelled: "warn"}[st] || "idle"}">${esc(STATUS_WORD[st] || st)}</span>`;
const DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
function fmtTime(ts) {
  if (!ts) return "—";
  const d = new Date(ts * 1000), n = new Date();
  const time = d.toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"});
  const day = new Date(d); day.setHours(0, 0, 0, 0);
  const today = new Date(n); today.setHours(0, 0, 0, 0);
  const diff = Math.round((day - today) / 864e5);
  if (diff === 0) return `Today ${time}`;
  if (diff === -1) return `Yesterday ${time}`;
  if (diff === 1) return `Tomorrow ${time}`;
  return d.toLocaleDateString([], {month: "short", day: "numeric", year: d.getFullYear() !== n.getFullYear() ? "numeric" : undefined}) + " " + time;
}
function dur(a, b) {
  if (!a) return "—";
  let s = Math.max(0, Math.round((b || Date.now() / 1000) - a));
  const h = Math.floor(s / 3600); s -= h * 3600; const m = Math.floor(s / 60); s -= m * 60;
  return h ? `${h}h ${m}m` : m ? `${m}m ${s}s` : `${s}s`;
}
function bytes(n) {
  if (n == null) return "—";
  const u = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"]; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return `${n.toFixed(n < 10 && i ? 1 : 0)} ${u[i]}`;
}
function schedText(s) {
  if (!s || s.type === "manual") return "Only when started by hand";
  const mm = s.time.split(":")[1];
  if (s.type === "hourly") return s.interval_hours === 1 ? `Every hour at :${mm}` : `Every ${s.interval_hours} hours at :${mm}`;
  const days = s.days || [];
  if (days.length === 7) return `Daily at ${s.time}`;
  if (days.join() === "0,1,2,3,4") return `Weekdays at ${s.time}`;
  if (days.join() === "5,6") return `Weekends at ${s.time}`;
  return `${days.map(d => DAYS[d]).join(", ")} at ${s.time}`;
}
const lastFinished = j => (j.recent || []).find(r => r.status !== "running");
const isRunning = j => (j.recent || []).some(r => r.status === "running");

function tape(recent, clientId) {
  const slots = 20, runs = (recent || []).slice(0, slots).reverse();
  let html = "";
  for (let i = 0; i < slots - runs.length; i++) html += `<span aria-hidden="true"></span>`;
  for (const r of runs) {
    const label = `${STATUS_WORD[r.status] || r.status}, ${fmtTime(r.started)}, ${r.destination_name}`;
    html += `<a class="${esc(r.status)}" href="#/activity/${esc(clientId)}/${esc(r.id)}" title="${esc(label)}" aria-label="${esc(label)}"></a>`;
  }
  return `<div class="tape">${html}</div>`;
}
function jobState(j) {
  const cur = (j.recent || []).find(r => r.status === "running");
  if (cur) return `<div class="state running"><b>Running now</b><span class="sub">to ${esc(cur.destination_name)} for ${dur(cur.started)}</span></div>`;
  const last = lastFinished(j);
  if (!last) return `<div class="state"><b>Never run</b><span class="sub">No history yet</span></div>`;
  return `<div class="state ${esc(last.status)}"><b>${STATUS_WORD[last.status]}</b><span class="sub">${esc(ago(last.ended))}</span></div>`;
}
function nextText(j) {
  if (!j.enabled) return `<b>Disabled</b><div class="sub">Enable the job to run it</div>`;
  if (!j.next_run) return `<b>By hand</b><div class="sub">Start it with Run now</div>`;
  return `<b>${esc(fmtTime(j.next_run))}</b><div class="sub">${esc(schedText(j.schedule))}</div>`;
}
function jobLedger(jobs, sizes, opts = {}) {
  return `<div class="ledger">
    <div class="ledger-head"><div>Job</div><div>Last 20 runs, oldest to newest</div><div>Last result</div><div>Next run</div><div></div></div>
    ${jobs.map(j => `<div class="jobrow ${j.enabled ? "" : "disabled"}">
      <div><a class="jobname" href="#/jobs/${esc(j.id)}">${esc(j.name)}</a>
        <div class="sub">${opts.noClient ? "To" : esc(j.client_name) + " to"} ${esc(j.destination_names.join(", ") || "no destination")}</div>
        ${sizes && jobSize(sizes.jobs[j.id]) ? `<div class="sub">${esc(jobSize(sizes.jobs[j.id]))}</div>` : ""}</div>
      ${tape(j.recent, j.client_id)}
      ${jobState(j)}
      <div class="small">${nextText(j)}</div>
      <div class="right">${runButton(j)}</div>
    </div>`).join("")}</div>`;
}
function runButton(j) {
  // A running backup can always be cancelled; a disabled job can't be started.
  if (isRunning(j)) return `<button class="btn small" data-cancel="${esc(j.id)}">Cancel</button>`;
  if (!j.enabled) return `<button class="btn small" disabled title="This job is disabled. Enable it to run it." aria-label="Run now (unavailable: this job is disabled)">Run now</button>`;
  return `<button class="btn small" data-run="${esc(j.id)}">Run now</button>`;
}
// switchHtml is an on/off switch: a checkbox with role="switch", drawn by CSS.
// With a job ID it turns that job on or off straight away (see bindSwitches).
function switchHtml(name, on, jobId) {
  return `<label class="switch"><input type="checkbox" role="switch" name="${esc(name)}" ${on ? "checked" : ""}${jobId ? ` data-enable="${esc(jobId)}"` : ""}>
    <span class="track" aria-hidden="true"></span><span class="switch-label">${on ? "Enabled" : "Disabled"}</span></label>`;
}
const jobSwitch = j => switchHtml("job-enabled", j.enabled, j.id);
function bindSwitches(refresh) {
  $$(".switch input").forEach(i => i.addEventListener("change", () => { $(".switch-label", i.parentElement).textContent = i.checked ? "Enabled" : "Disabled"; }));
  $$("[data-enable]").forEach(i => i.addEventListener("change", async () => {
    i.disabled = true;
    try {
      await api("POST", `/jobs/${i.dataset.enable}/enabled`, {enabled: i.checked});
      toast(i.checked ? "Job enabled. Its schedule is sent to the client." : "Job disabled. It won't run until you enable it again.");
      refresh && refresh();
    } catch (ex) {
      toast(ex.message, "bad");
      i.checked = !i.checked;
      $(".switch-label", i.parentElement).textContent = i.checked ? "Enabled" : "Disabled";
    } finally { i.disabled = false; }
  }));
}

function bindRunButtons(refresh) {
  $$("[data-run]").forEach(b => b.addEventListener("click", async () => {
    b.disabled = true;
    try { await api("POST", `/jobs/${b.dataset.run}/run`); toast("Backup started. Results appear here in a few seconds."); setTimeout(refresh, 3500); }
    catch (ex) { toast(ex.message, "bad"); b.disabled = false; }
  }));
  $$("[data-cancel]").forEach(b => b.addEventListener("click", async () => {
    if (!confirm("Stop this backup? The partial snapshot will be discarded.")) return;
    b.disabled = true;
    try { await api("POST", `/jobs/${b.dataset.cancel}/cancel`); toast("Stopping the backup."); setTimeout(refresh, 3500); }
    catch (ex) { toast(ex.message, "bad"); b.disabled = false; }
  }));
}

async function viewJobs(token) {
  const [{jobs}, {clients}, {destinations}, {sizes}] = await Promise.all([api("GET", "/jobs"), api("GET", "/clients"), api("GET", "/destinations"), api("GET", "/sizes")]);
  if (token !== routeToken) return;
  const missing = !clients.length ? ["a client", "#/clients/new", "Add a client"] : !destinations.length ? ["a destination", "#/destinations/new", "Add a destination"] : null;
  render(`<div class="pagehead"><div><h1>Backup jobs</h1><p class="lede">Each job backs up folders on one client to one or more destinations, on its own schedule.</p></div>
    ${missing ? "" : `<a class="btn primary" href="#/jobs/new">Create a backup job</a>`}</div>
    ${jobs.length ? jobLedger(jobs, sizes) : `<div class="panel empty"><h2>No jobs yet</h2><p>${missing ? `Add ${missing[0]} first, so jobs have something to back up${missing[0] === "a client" ? "" : " to"}.` : "Create a job to choose folders and a schedule."}</p>
      <a class="btn primary" href="${missing ? missing[1] : "#/jobs/new"}">${missing ? missing[2] : "Create a backup job"}</a></div>`}`);
  const refresh = async () => { if (token === routeToken) route(); };
  bindRunButtons(refresh);
  poll(async () => {
    const [d, z] = await Promise.all([api("GET", "/jobs"), api("GET", "/sizes")]);
    if (token === routeToken && $(".ledger")) { $(".ledger").outerHTML = jobLedger(d.jobs, z.sizes); bindRunButtons(refresh); }
  }, 8000);
}

function runsTable(items, showJob) {
  if (!items.length) return `<p class="muted">No runs yet.</p>`;
  return `<table><thead><tr><th>Result</th>${showJob ? "<th>Job</th>" : ""}<th>Destination</th><th>Started</th><th>Took</th><th>Details</th></tr></thead><tbody>
    ${items.map(({run: r, client_name}) => `<tr class="clickable" data-href="#/activity/${esc(r.client_id)}/${esc(r.id)}">
      <td>${runPill(r.status)}</td>${showJob ? `<td><a class="jobname" href="#/activity/${esc(r.client_id)}/${esc(r.id)}">${esc(r.job_name)}</a><div class="sub">${esc(client_name)}, ${r.trigger === "manual" ? "started by hand" : "scheduled"}</div></td>` : ""}
      <td class="small">${esc(r.destination_name)}</td>
      <td class="small nowrap">${esc(fmtTime(r.started))}</td>
      <td class="small nowrap">${esc(dur(r.started, r.ended || null))}</td>
      <td><div class="summary">${esc(r.summary || (r.status === "running" ? "In progress" : ""))}</div>${statsLine(r.stats) ? `<div class="sub">${esc(statsLine(r.stats))}</div>` : ""}</td></tr>`).join("")}
  </tbody></table>`;
}

async function viewJobDetail(id, token) {
  const [{job: j}] = await Promise.all([api("GET", `/jobs/${id}`)]);
  if (token !== routeToken) return;
  render(`<a class="back" href="#/jobs">‹ Backup jobs</a>
    <div class="pagehead"><div><div class="titleline"><h1>${esc(j.name)}</h1><span id="j-switch">${jobSwitch(j)}</span></div>
      <p class="lede"><a href="#/clients/${esc(j.client_id)}">${esc(j.client_name)}</a> · ${esc(schedText(j.schedule))}</p></div>
      <div class="btnrow"><a class="btn" href="#/jobs/${esc(id)}/edit">Edit job</a><span id="j-run">${runButton(j).replace("btn small", "btn primary")}</span></div></div>
    <div class="cols"><div class="stack">
      <div class="panel"><h2>Recent runs</h2><div id="runs" class="tablewrap"><div class="loading">Loading…</div></div></div>
      <div class="panel"><h2>Snapshots on the server</h2><div id="snaps"><div class="loading">Asking the server…</div></div></div>
    </div>
    <div class="stack">
    <div class="panel"><h2>Size</h2><div id="jsize"><div class="loading">Loading…</div></div></div>
    <div class="panel"><h2>Details</h2><dl class="kv">
      <dt>Client</dt><dd><a href="#/clients/${esc(j.client_id)}">${esc(j.client_name)}</a></dd>
      <dt>Destinations</dt><dd>${j.destination_names.map(esc).join("<br>") || `<span class="bad-text">None</span>`}</dd>
      <dt>Backup ID</dt><dd class="mono">host/${esc(j.backup_id)}</dd>
      <dt>Next run</dt><dd>${j.next_run ? esc(fmtTime(j.next_run)) : `<span class="muted">${j.enabled ? "Only by hand" : "Disabled"}</span>`}</dd>
      <dt>Folders</dt><dd>${j.shares.map(s => `<div><span class="mono">${esc(s.path)}</span><div class="sub">saved as ${esc(s.archive)}.pxar</div></div>`).join("")}</dd>
      <dt>Excluded</dt><dd>${j.excludes.length ? j.excludes.map(e => `<div class="mono">${esc(e)}</div>`).join("") : `<span class="muted">Nothing</span>`}</dd>
      <dt>Change detection</dt><dd>${esc({metadata: "Metadata (fastest)", data: "Data", legacy: "Legacy"}[j.change_detection])}</dd>
      <dt>Speed limit</dt><dd>${j.rate ? esc(j.rate) + "/s" : `<span class="muted">None</span>`}</dd>
      <dt>Encryption</dt><dd>${j.keyfile ? `<span class="mono">${esc(j.keyfile)}</span>` : `<span class="muted">Not encrypted by this job</span>`}</dd>
    </dl></div></div></div>`);
  const drawSize = sz => {
    const js = sz.jobs[id];
    if (!js) return;
    const folders = js.folder_bytes == null
      ? `<span class="muted">${js.measuring ? "Measuring now. Large folders can take a while." : "Not measured yet."}</span>`
      : `<b>${esc(jobSize(js))}</b> <span class="sub">measured ${esc(ago(js.folder_measured))}${js.measuring ? ", measuring again now" : ""}</span>`;
    const rows = j.destinations.map((did, i) => {
      const l = js.backups[did], name = j.destination_names[i] || did;
      const v = !l ? `<span class="muted">Not checked yet</span>` : l.error ? `<span class="bad-text">${esc(l.error)}</span>`
        : !l.count ? `<span class="muted">No backups yet</span>` : `${esc(bytes(l.bytes))} <span class="sub">from ${esc(fmtTime(l.time))}</span>`;
      return `<dt>${esc(name)}</dt><dd>${v}</dd>`;
    }).join("");
    $("#jsize").innerHTML = `<dl class="kv"><dt>Folders</dt><dd>${folders}${js.folder_errors.map(e => `<div class="sub bad-text">${esc(e)}</div>`).join("")}</dd>
      ${rows}</dl>
      <p class="hint">Backup sizes are the newest snapshot as PBS counts it, before deduplication. Unchanged data is only stored once, so it takes less room.</p>
      <div class="btnrow"><button class="btn small" data-measure="${esc(j.client_id)}">Measure again</button><button class="btn small" data-checkspace="">Check backups now</button></div>`;
    bindSizeButtons(() => api("GET", "/sizes").then(r => { if (token === routeToken) drawSize(r.sizes); }), id);
  };
  let lastSizes = "";
  const drawRuns = async () => {
    const [{runs}, {job}, {sizes}] = await Promise.all([api("GET", `/runs?job=${id}&limit=25`), api("GET", `/jobs/${id}`), api("GET", "/sizes")]);
    if (token !== routeToken) return;
    // Only redraw sizes when they change, so the buttons don't flicker.
    const key = JSON.stringify(sizes.jobs[id] || null);
    if (key !== lastSizes) { lastSizes = key; drawSize(sizes); }
    $("#runs").innerHTML = runsTable(runs, false);
    $("#j-run").innerHTML = runButton(job).replace("btn small", "btn primary");
    // Follow changes made elsewhere, unless the switch is being used.
    const sw = $("#j-switch input");
    if (sw && sw.checked !== job.enabled && document.activeElement !== sw) { route(); return; }
    bindRowLinks();
    bindRunButtons(drawRuns);
  };
  // The schedule, next run and Run now all change with it, so redraw the page.
  bindSwitches(() => { if (token === routeToken) route(); });
  await drawRuns();
  poll(drawRuns, 5000);
  api("GET", `/jobs/${id}/snapshots`).then(({destinations}) => {
    if (token !== routeToken) return;
    $("#snaps").innerHTML = destinations.map(d => `<h3 class="mt-12">${esc(d.destination_name)}</h3>
      <p class="hint m-0 mb-14">Stored in <span class="mono">${d.namespace ? esc(d.namespace) + "/" : ""}${esc(d.group)}</span>. Restore files from the PBS web interface or with <span class="mono">proxmox-backup-client restore</span>.</p>
      ${d.error ? `<div class="result bad">${esc(d.error)}</div>` : d.snapshots.length ? `<div class="tablewrap"><table><thead><tr><th>Taken</th><th>Size</th><th>Verified</th></tr></thead><tbody>
        ${d.snapshots.map(s => `<tr><td>${esc(fmtTime(s.time))}${s.protected ? ` <span class="pill busy">Protected</span>` : ""}</td><td>${esc(bytes(s.size))}</td>
        <td>${s.verified === "ok" ? `<span class="pill ok">OK</span>` : s.verified === "failed" ? `<span class="pill bad">Failed</span>` : `<span class="muted small">Not yet</span>`}</td></tr>`).join("")}
        </tbody></table></div>` : `<p class="muted">No snapshots yet. They appear after the first successful run.</p>`}`).join("") || `<p class="muted">No destinations.</p>`;
  }).catch(ex => { if (token === routeToken) $("#snaps").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; });
}

async function viewJobForm(id, token, presetClient) {
  const [{clients}, {destinations}, existing] = await Promise.all([api("GET", "/clients"), api("GET", "/destinations"), id ? api("GET", `/jobs/${id}`) : null]);
  if (token !== routeToken) return;
  if (!clients.length || !destinations.length) {
    render(`<a class="back" href="#/jobs">‹ Backup jobs</a><div class="panel empty"><h2>${clients.length ? "Add a destination first" : "Add a client first"}</h2>
      <p>${clients.length ? "A job needs a Proxmox Backup Server datastore to send its backups to." : "A job backs up folders on a client."}</p>
      <a class="btn primary" href="${clients.length ? "#/destinations/new" : "#/clients/new"}">${clients.length ? "Add a destination" : "Add a client"}</a></div>`);
    return;
  }
  const job = existing && existing.job;
  const j = job || {name: "", client_id: presetClient || (clients.length === 1 ? clients[0].id : ""), backup_id: "", shares: [], excludes: [],
    schedule: {type: "daily", time: "02:00", days: [0, 1, 2, 3, 4, 5, 6], interval_hours: 6}, change_detection: "metadata", rate: "", keyfile: "",
    enabled: true, destinations: destinations.length === 1 ? [destinations[0].id] : []};
  const shares = j.shares.length ? j.shares.map(s => ({...s, auto: false})) : [{path: "", archive: "", auto: true}];
  const s = j.schedule;
  const clientName = cid => (clients.find(c => c.id === cid) || {}).name || "";
  render(`<a class="back" href="${id ? `#/jobs/${esc(id)}` : "#/jobs"}">‹ ${id ? esc(j.name) : "Backup jobs"}</a>
    <h1>${id ? "Edit backup job" : "Create a backup job"}</h1>
    <p class="lede">Pick a client, the folders to protect, where they go, and when. The client gets the job straight away and runs it on its own schedule.</p>
    <form id="jobform" class="panel" novalidate>
      <fieldset class="section"><legend>Basics</legend>
        <div class="formgrid top">
          <label class="field"><span>Client</span>${id ? `<input type="text" value="${esc(clientName(j.client_id))}" disabled><input type="hidden" name="client_id" value="${esc(j.client_id)}">`
            : `<select name="client_id"><option value="">Choose a client…</option>${clients.map(c => `<option value="${esc(c.id)}" ${c.id === j.client_id ? "selected" : ""}>${esc(c.name)}</option>`).join("")}</select>`}</label>
          <label class="field"><span>Job name</span><input type="text" name="name" value="${esc(j.name)}" placeholder="Media shares" maxlength="64"></label>
          <div class="field full"><span>Back up to</span><div class="choice">${destinations.map(d => `<label class="check"><input type="checkbox" name="dest" value="${esc(d.id)}" ${j.destinations.includes(d.id) ? "checked" : ""}><span><b>${esc(d.name)}</b> <span class="sub mono">${esc(d.repository)}${d.namespace ? " · " + esc(d.namespace) : ""}</span></span></label>`).join("")}</div>
            <small>Each destination is backed up in turn. For an offsite copy, a sync job in PBS (one PBS pulling from another) reads the client's files only once.</small></div>
          <label class="field"><span>Backup ID</span><input type="text" name="backup_id" value="${esc(j.backup_id)}" class="mono" placeholder="The client's host name">
            <small>The group name on the server (host/<i>id</i>). Keep it the same so each run builds on the last one.</small></label>
          <div class="field full">${switchHtml("enabled", j.enabled, "")}<small>A disabled job doesn't run, on its schedule or with Run now.</small></div>
        </div>
      </fieldset>
      <fieldset class="section"><legend>Folders to back up</legend>
        <p class="hint">Each folder becomes its own archive. Keep archive names unchanged between runs so unchanged data isn't sent again.</p>
        <div class="share-head"><span>Folder</span><span></span><span>Archive name</span><span></span></div>
        <div class="shares" id="shares"></div>
        <button type="button" class="btn small mt-12" id="addshare">Add another folder</button>
      </fieldset>
      <fieldset class="section"><legend>Schedule</legend>
        <div class="seg mt-12" role="radiogroup" aria-label="How often">
          ${[["manual", "By hand only"], ["daily", "On certain days"], ["hourly", "Every few hours"]].map(([v, l]) => `<label><input type="radio" name="stype" value="${v}" ${s.type === v ? "checked" : ""}>${l}</label>`).join("")}
        </div>
        <div id="sched-daily" class="formgrid mt-16"><div class="field full"><span>Days</span><div class="days">${DAYS.map((d, i) => `<label><input type="checkbox" name="day" value="${i}" ${(s.days || []).includes(i) ? "checked" : ""}>${d}</label>`).join("")}</div></div></div>
        <div id="sched-hourly" class="formgrid mt-16"><label class="field"><span>Every</span><select name="interval">${[1, 2, 3, 4, 6, 8, 12].map(n => `<option value="${n}" ${s.interval_hours === n ? "selected" : ""}>${n === 1 ? "hour" : n + " hours"}</option>`).join("")}</select></label></div>
        <div id="sched-time" class="formgrid mt-16"><label class="field"><span id="time-label">Start time</span><input type="time" name="time" value="${esc(s.time)}"><small id="time-hint"></small></label></div>
        <p class="hint mt-12" id="sched-summary"></p>
      </fieldset>
      <fieldset class="section"><details class="adv" ${j.excludes.length || j.rate || j.keyfile || j.change_detection !== "metadata" ? "open" : ""}><summary>More options</summary>
        <div class="formgrid">
          <label class="field full"><span>Skip these files and folders</span><textarea name="excludes" placeholder="lost+found&#10;**/.recycle&#10;**/*.tmp">${esc(j.excludes.join("\n"))}</textarea>
            <small>One pattern per line, relative to each folder. Use ** to match any depth.</small></label>
          <label class="field"><span>Change detection</span><select name="mode">
            <option value="metadata" ${j.change_detection === "metadata" ? "selected" : ""}>Metadata: skip files whose size and time are unchanged</option>
            <option value="data" ${j.change_detection === "data" ? "selected" : ""}>Data: read every file each run</option>
            <option value="legacy" ${j.change_detection === "legacy" ? "selected" : ""}>Legacy: single-archive format</option></select>
            <small>Metadata is much faster for large, mostly unchanged shares.</small></label>
          <label class="field"><span>Upload speed limit</span><input type="text" name="rate" value="${esc(j.rate)}" placeholder="No limit, e.g. 20MiB"><small>Per second.</small></label>
          <label class="field"><span>Encryption key file on the client</span><input type="text" name="keyfile" value="${esc(j.keyfile)}" class="mono" placeholder="/root/pbs.key">
            <small>Optional. Create one with <span class="mono">proxmox-backup-client key create</span> and keep a copy somewhere other than the client.</small></label>
          <label class="field"><span>Key file password</span><input type="password" name="keyfile_password" autocomplete="new-password" placeholder="${job && job.keyfile_password_set ? "Saved. Leave blank to keep it" : "Only if the key has one"}">
            <small>Stored encrypted on the client, never shown again.</small></label>
        </div></details>
      </fieldset>
      <div id="form-error" class="result bad hidden" role="alert"></div>
      <div class="formfoot">
        <div class="btnrow"><button class="btn primary" type="submit">${id ? "Save changes" : "Create job"}</button><a class="btn" href="${id ? `#/jobs/${esc(id)}` : "#/jobs"}">Cancel</a></div>
        ${id ? `<button type="button" class="btn danger" id="deljob">Delete job</button>` : ""}
      </div>
    </form>`);

  const form = $("#jobform");
  bindSwitches();
  const archiveFrom = p => { const b = (p.replace(/\/+$/, "").split("/").pop() || "root").replace(/[^A-Za-z0-9_-]+/g, "-").replace(/^[-_]+|[-_]+$/g, ""); return (b || "root").slice(0, 48); };
  const drawShares = () => {
    $("#shares").innerHTML = shares.map((sh, i) => `<div class="share">
      <input type="text" class="mono" data-i="${i}" data-k="path" value="${esc(sh.path)}" placeholder="/srv/dev-disk-by-uuid-…/Media" aria-label="Folder path">
      <button type="button" class="btn small" data-browse="${i}">Browse</button>
      <input type="text" class="mono" data-i="${i}" data-k="archive" value="${esc(sh.archive)}" placeholder="archive name" aria-label="Archive name">
      <button type="button" class="iconbtn" data-del="${i}" aria-label="Remove folder" ${shares.length === 1 ? "disabled" : ""}>×</button></div>`).join("");
    $$("#shares input").forEach(inp => inp.addEventListener("input", () => {
      const sh = shares[inp.dataset.i];
      sh[inp.dataset.k] = inp.value;
      if (inp.dataset.k === "archive") sh.auto = false;
      if (inp.dataset.k === "path" && sh.auto) { sh.archive = archiveFrom(inp.value); inp.parentNode.querySelector("[data-k=archive]").value = sh.archive; }
    }));
    $$("[data-del]").forEach(b => b.addEventListener("click", () => { shares.splice(+b.dataset.del, 1); drawShares(); }));
    $$("[data-browse]").forEach(b => b.addEventListener("click", async () => {
      const cid = form.client_id.value;
      if (!cid) { toast("Choose the client first.", "bad"); return; }
      const sh = shares[+b.dataset.browse];
      const picked = await pickFolder(cid, sh.path || "/", true);
      if (picked) { sh.path = picked; if (sh.auto || !sh.archive) { sh.archive = archiveFrom(picked); sh.auto = true; } drawShares(); }
    }));
  };
  drawShares();
  $("#addshare").addEventListener("click", () => { shares.push({path: "", archive: "", auto: true}); drawShares(); $$("#shares input[data-k=path]").pop().focus(); });
  const readSched = () => ({type: form.stype.value, time: form.time.value || "02:00", days: $$("[name=day]:checked", form).map(c => +c.value), interval_hours: +form.interval.value});
  const syncSched = () => {
    const t = form.stype.value;
    $("#sched-daily").classList.toggle("hidden", t !== "daily");
    $("#sched-hourly").classList.toggle("hidden", t !== "hourly");
    $("#sched-time").classList.toggle("hidden", t === "manual");
    $("#time-label").textContent = t === "hourly" ? "Starting at" : "Start time";
    const tz = (clients.find(c => c.id === form.client_id.value) || {}).timezone;
    $("#time-hint").textContent = t === "hourly" ? "Runs at this minute past the hour, on hours that divide evenly by the interval." : `In the client's time zone${tz ? " (" + tz + ")" : ""}.`;
    $("#sched-summary").textContent = t === "manual" ? "This job only runs when you press Run now." : "Summary: " + schedText(readSched());
  };
  form.addEventListener("change", syncSched);
  syncSched();
  form.addEventListener("submit", async e => {
    e.preventDefault();
    const err = $("#form-error"); err.classList.add("hidden");
    const payload = {client_id: form.client_id.value, name: form.name.value, backup_id: form.backup_id.value, enabled: form.enabled.checked,
      destinations: $$("[name=dest]:checked", form).map(c => c.value),
      shares: shares.filter(sh => sh.path.trim()).map(sh => ({path: sh.path.trim(), archive: sh.archive.trim()})),
      schedule: readSched(), excludes: form.excludes.value, change_detection: form.mode.value, rate: form.rate.value,
      keyfile: form.keyfile.value, keyfile_password: form.keyfile_password.value};
    const btn = $("button[type=submit]", form); btn.disabled = true;
    try {
      const r = id ? await api("PUT", `/jobs/${id}`, payload) : await api("POST", "/jobs", payload);
      toast(id ? "Changes saved. Sending them to the client." : "Job created. Sending it to the client.");
      location.hash = `#/jobs/${r.job.id}`;
    } catch (ex) { showError(err, ex.message); err.scrollIntoView({block: "center"}); }
    finally { btn.disabled = false; }
  });
  if (id) $("#deljob").addEventListener("click", async () => {
    if (!confirm(`Delete “${j.name}”? Its schedule is removed from the client. Snapshots already on the server are kept.`)) return;
    try { await api("DELETE", `/jobs/${id}`); toast("Job deleted."); location.hash = "#/jobs"; }
    catch (ex) { toast(ex.message, "bad"); }
  });
}

/* ---------- destinations ---------- */
function usageHtml(u) {
  if (!u || u.total == null) return `<div class="result ok">Connected. The datastore answered.</div>`;
  const pct = u.total ? Math.round(u.used / u.total * 100) : 0;
  return `<div class="result ok">Connected. ${bytes(u.used)} of ${bytes(u.total)} used (${pct}%), ${bytes(u.avail)} free.</div>
    <div class="meter ${spaceClass(pct)}" data-pct="${pct}"><i></i></div>`;
}
function sizeMeters(root) { $$(".meter[data-pct]", root).forEach(m => { $("i", m).style.width = Math.min(100, +m.dataset.pct) + "%"; }); }

async function viewDestinations(token) {
  const [{destinations}, {sizes}] = await Promise.all([api("GET", "/destinations"), api("GET", "/sizes")]);
  if (token !== routeToken) return;
  render(`<div class="pagehead"><div><h1>Destinations</h1><p class="lede">The Proxmox Backup Server datastores jobs send backups to, with the credentials clients use to reach them.</p></div>
    <a class="btn primary" href="#/destinations/new">Add a destination</a></div>
    ${destinations.length ? `<div class="stack">${destinations.map(d => `<div class="panel"><div class="pagehead m-0">
      <div><h3><a class="jobname" href="#/destinations/${esc(d.id)}">${esc(d.name)}</a></h3><div class="sub mono">${esc(d.repository)}${d.namespace ? " · namespace " + esc(d.namespace) : ""}</div>
        <div class="sub">${d.used_by.length ? `Used by ${esc(d.used_by.join(", "))}` : "Not used by any job"}${d.fingerprint ? "" : ", no fingerprint saved"}</div></div>
      <div class="btnrow"><button class="btn small" data-check="${esc(d.id)}">Check connection</button><a class="btn small" href="#/destinations/${esc(d.id)}">Edit</a></div></div>
      <div class="mt-12">${spaceHtml(sizes.destinations[d.id], d.name)}${sizes.destinations[d.id] && sizes.destinations[d.id].checked ? `<div class="sub">Checked ${esc(ago(sizes.destinations[d.id].checked))}</div>` : ""}</div>
      <div id="u-${esc(d.id)}" class="mt-12"></div></div>`).join("")}</div>`
      : `<div class="panel empty"><h2>No destinations yet</h2><p>Add the PBS server and datastore to back up to, with an API token for it. For one token per client, add a destination per client pointing at the same datastore.</p><a class="btn primary" href="#/destinations/new">Add a destination</a></div>`}`);
  sizeMeters($("#view"));
  $$("[data-check]").forEach(b => b.addEventListener("click", async () => {
    const d = destinations.find(x => x.id === b.dataset.check), box = $(`#u-${d.id}`);
    b.disabled = true; box.innerHTML = `<div class="result info">Connecting…</div>`;
    try { box.innerHTML = usageHtml((await api("POST", "/destinations/test", {...d, secret: ""})).usage); sizeMeters(box); api("POST", "/sizes/check", {destination: d.id}).catch(() => {}); }
    catch (ex) { box.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { b.disabled = false; }
  }));
}

async function viewDestinationForm(id, token) {
  const {destinations} = await api("GET", "/destinations");
  if (token !== routeToken) return;
  const t = id ? destinations.find(x => x.id === id) : null;
  if (id && !t) throw new Error("That destination doesn't exist anymore.");
  const v = t || {name: "", host: "", port: 8007, datastore: "", namespace: "", username: "", token_name: "", fingerprint: ""};
  render(`<a class="back" href="#/destinations">‹ Destinations</a>
    <h1>${id ? "Edit destination" : "Add a destination"}</h1>
    <p class="lede">In PBS, create a user and an API token, and give both the DatastoreBackup role on the datastore (or on a namespace in it). The fingerprint is under Dashboard, Show Fingerprint.</p>
    <form id="tform" class="panel" novalidate>
      <fieldset class="section"><legend>Server</legend>
        <div class="formgrid top">
          <label class="field full"><span>Name</span><input type="text" name="name" value="${esc(v.name)}" placeholder="Home PBS" maxlength="64"></label>
          <label class="field"><span>Host or IP address</span><input type="text" name="host" value="${esc(v.host)}" placeholder="pbs.lan"></label>
          <label class="field"><span>Port</span><input type="number" name="port" value="${esc(v.port)}" min="1" max="65535"></label>
          <label class="field"><span>Datastore</span><input type="text" name="datastore" value="${esc(v.datastore)}" placeholder="store1"></label>
          <label class="field"><span>Namespace</span><input type="text" name="namespace" value="${esc(v.namespace)}" class="mono" placeholder="Datastore root">
            <small>Optional, like <span class="mono">clients/nas</span>. It must already exist in PBS.</small></label>
          <label class="field full"><span>Fingerprint</span><input type="text" name="fingerprint" value="${esc(v.fingerprint)}" class="mono" placeholder="ab:cd:ef:…">
            <small>Needed if the server uses its default self-signed certificate. Without it, backups fail.</small></label>
        </div>
      </fieldset>
      <fieldset class="section"><legend>Credentials</legend>
        <div class="formgrid top">
          <label class="field"><span>User</span><input type="text" name="username" value="${esc(v.username)}" placeholder="nas-backup@pbs" autocomplete="off"><small>Include the realm after the @.</small></label>
          <label class="field"><span>API token name</span><input type="text" name="token_name" value="${esc(v.token_name)}" placeholder="nas" autocomplete="off"><small>Just the part after the !. Leave blank to use the user's password instead.</small></label>
          <label class="field full"><span>Token secret</span><input type="password" name="secret" autocomplete="new-password" placeholder="${t && t.secret_set ? "Saved. Leave blank to keep it" : "Shown once when you created the token"}">
            <small>Stored encrypted on this server and on each client that uses it. It's never shown again.</small></label>
        </div>
      </fieldset>
      <div id="t-result"></div>
      <div class="formfoot">
        <div class="btnrow"><button class="btn primary" type="submit">${id ? "Save changes" : "Add destination"}</button><button type="button" class="btn" id="ttest">Test connection</button><a class="btn" href="#/destinations">Cancel</a></div>
        ${id ? `<button type="button" class="btn danger" id="tdel">Delete destination</button>` : ""}
      </div>
    </form>`);
  const form = $("#tform"), out = $("#t-result");
  const read = () => ({id: id || undefined, name: form.name.value, host: form.host.value, port: Number(form.port.value) || 0, datastore: form.datastore.value,
    namespace: form.namespace.value, fingerprint: form.fingerprint.value, username: form.username.value, token_name: form.token_name.value, secret: form.secret.value});
  $("#ttest").addEventListener("click", async e => {
    e.target.disabled = true; out.innerHTML = `<div class="result info">Connecting…</div>`;
    try { out.innerHTML = usageHtml((await api("POST", "/destinations/test", read())).usage); sizeMeters(out); }
    catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { e.target.disabled = false; }
  });
  form.addEventListener("submit", async e => {
    e.preventDefault(); out.innerHTML = "";
    const btn = $("button[type=submit]", form); btn.disabled = true;
    try {
      id ? await api("PUT", `/destinations/${id}`, read()) : await api("POST", "/destinations", read());
      toast(id ? "Changes saved. Clients using it get the update." : "Destination added.");
      location.hash = "#/destinations";
    } catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { btn.disabled = false; }
  });
  if (id) $("#tdel").addEventListener("click", async () => {
    if (!confirm(`Delete “${v.name}”? This only removes it from PBC Manager. Nothing on the PBS server is touched.`)) return;
    try { await api("DELETE", `/destinations/${id}`); toast("Destination deleted."); location.hash = "#/destinations"; }
    catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
  });
}

/* ---------- activity ---------- */
async function viewActivity(token) {
  render(`<h1>Activity</h1><p class="lede">Every backup run on every client, newest first. Open one to read its log.</p><div class="panel tablewrap" id="act"><div class="loading">Loading…</div></div>`);
  const draw = async () => {
    const {runs} = await api("GET", "/runs?limit=200");
    if (token !== routeToken) return;
    $("#act").innerHTML = runsTable(runs, true);
    bindRowLinks();
  };
  await draw();
  poll(draw, 8000);
}

// statsLine is a run's figures in a few words, for lists.
function statsLine(st) {
  if (!st || !(st.known || []).includes("sizes")) return "";
  const parts = [`Read ${bytes(st.read)}`, `uploaded ${bytes(st.uploaded)}`];
  if (st.read > 0 && (st.known || []).includes("reused")) parts.push(`reused ${Math.round(st.reused * 100 / st.read)}%`);
  return parts.join(" · ");
}
// statsPanel is the run page's figures. Each row is a value and an optional
// note, both escaped here.
function statsPanel(st) {
  if (!st || !(st.known || []).length) return "";
  const has = x => st.known.includes(x), rows = [];
  if (has("sizes")) {
    rows.push(["Data read", bytes(st.read), ""]);
    rows.push(["Uploaded", `${bytes(st.uploaded)} new data`, `${bytes(st.compressed)} compressed`]);
  }
  if (has("reused") && st.read > 0) rows.push(["Reused", bytes(st.reused), `from the last backup (${Math.round(st.reused * 100 / st.read)}%)`]);
  if (has("files")) rows.push(["Files", String(st.files), `${st.changed} new or changed`]);
  if (has("duration") && st.seconds > 0) rows.push(["Upload time", st.seconds < 1 ? "Under a second" : secs(Math.round(st.seconds)), ""]);
  const per = st.archives && st.archives.length > 1 ? `<div class="tablewrap mt-12"><table><thead><tr><th>Archive</th><th>Read</th><th>Uploaded</th><th>Reused</th></tr></thead><tbody>
    ${st.archives.map(a => `<tr><td class="mono small">${esc(a.name)}</td><td class="small">${esc(bytes(a.read))}</td><td class="small">${esc(bytes(a.uploaded))}</td><td class="small">${esc(bytes(a.reused))}</td></tr>`).join("")}</tbody></table></div>` : "";
  return `<div class="panel mb-14"><h2>Backup figures</h2><dl class="kv">
    ${rows.map(([label, value, note]) => `<dt>${esc(label)}</dt><dd>${esc(value)}${note ? ` <span class="sub">${esc(note)}</span>` : ""}</dd>`).join("")}</dl>${per}</div>`;
}

async function viewRun(clientId, runId, token) {
  let offset = 0, logText = "";
  render(`<a class="back" href="#/activity">‹ Activity</a>
    <div class="pagehead"><div><h1 id="run-title">Run</h1><p class="lede" id="run-sub"></p></div><div class="btnrow" id="run-actions"></div></div>
    <div id="run-summary"></div>
    <pre class="log" id="log" tabindex="0" aria-label="Backup log"></pre>`);
  const logEl = $("#log");
  const draw = async () => {
    const {run: r, client_name} = await api("GET", `/runs/${clientId}/${runId}`);
    if (token !== routeToken) return;
    $("#run-title").innerHTML = `${esc(r.job_name)} ${runPill(r.status)}`;
    $("#run-sub").textContent = `${client_name} to ${r.destination_name}. ${r.trigger === "manual" ? "Started by hand" : "Scheduled"}, began ${fmtTime(r.started)}, ${r.status === "running" ? "running for " : "took "}${dur(r.started, r.ended || null)}.`;
    $("#run-actions").innerHTML = r.status === "running" ? `<button class="btn danger" data-cancel="${esc(r.job_id)}">Cancel run</button>` : `<a class="btn" href="#/jobs/${esc(r.job_id)}">View job</a>`;
    bindRunButtons(draw);
    $("#run-summary").innerHTML = (r.status === "failed" && r.summary ? `<div class="banner bad"><b>Why it failed:</b> ${esc(r.summary)}</div>` : "") + statsPanel(r.stats);
    try {
      const d = await api("GET", `/runs/${clientId}/${runId}/log?offset=${offset}`);
      if (token !== routeToken) return;
      if (d.text) {
        const atBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 40;
        logText += d.text; offset = d.offset;
        logEl.innerHTML = esc(logText).replace(/^.*\berror\b.*$/gim, m => `<span class="err">${m}</span>`);
        if (atBottom) logEl.scrollTop = logEl.scrollHeight;
      } else if (!logText) logEl.textContent = "No output yet.";
      if (d.done && r.status !== "running") stopPoll();
    } catch (ex) { if (!logText) logEl.textContent = ex.message; }
  };
  await draw();
  poll(draw, 2000);
}

/* ---------- clients ---------- */
const CLIENT_STATUS = {ready: ["ok", "Ready"], "setting-up": ["busy", "Setting up"], error: ["bad", "Needs attention"],
  unreachable: ["warn", "Can't connect"], "host-key-changed": ["bad", "Host key changed"]};
const clientPill = st => { const [cls, label] = CLIENT_STATUS[st] || ["warn", st]; return `<span class="pill ${cls}">${esc(label)}</span>`; };
function ago(ts) {
  if (!ts) return "never";
  const s = Math.round(Date.now() / 1000 - ts);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}
function hostKeyFile(type) {
  if ((type || "").startsWith("ecdsa")) return "/etc/ssh/ssh_host_ecdsa_key.pub";
  return {"ssh-ed25519": "/etc/ssh/ssh_host_ed25519_key.pub", "ssh-rsa": "/etc/ssh/ssh_host_rsa_key.pub"}[type] || "/etc/ssh/ssh_host_*_key.pub";
}
function bindRowLinks() {
  $$("tr[data-href]").forEach(tr => tr.addEventListener("click", e => { if (!e.target.closest("a,button")) location.hash = tr.dataset.href; }));
}
function clientsTable(clients) {
  return `<div class="panel tablewrap"><table>
    <thead><tr><th>Client</th><th>Status</th><th>System</th><th>Backup client</th><th>Last contact</th></tr></thead>
    <tbody>${clients.map(c => `<tr class="clickable" data-href="#/clients/${esc(c.id)}">
      <td><a class="jobname" href="#/clients/${esc(c.id)}">${esc(c.name)}</a><div class="sub mono">${esc(c.address)}${c.port === 22 ? "" : ":" + esc(c.port)}</div></td>
      <td>${clientPill(c.status)}</td>
      <td class="small">${esc(c.os_pretty || "—")}${c.arch ? `<div class="sub">${esc(c.arch)}</div>` : ""}</td>
      <td class="small">${c.client_version ? esc(c.client_version) : "—"}</td>
      <td class="small">${esc(ago(c.last_contact))}</td></tr>`).join("")}</tbody></table></div>`;
}

async function viewClients(token) {
  const {clients} = await api("GET", "/clients");
  if (token !== routeToken) return;
  render(`<div class="pagehead"><div><h1>Clients</h1><p class="lede">The machines this server manages. Each one keeps its own backup schedule, so it carries on if this server is down.</p></div>
    <a class="btn primary" href="#/clients/new">Add a client</a></div>
    ${clients.length ? clientsTable(clients) : `<div class="panel empty"><h2>No clients yet</h2><p>Add a Linux machine you can reach over SSH from this server, on your network or over a VPN.</p><a class="btn primary" href="#/clients/new">Add a client</a></div>`}`);
  bindRowLinks();
}

// Follows a task's log into box until it finishes, then calls done(view).
function followTask(taskId, box, token, done) {
  let offset = 0;
  const pre = $(".log", box) || (() => { const p = document.createElement("pre"); p.className = "log"; box.append(p); return p; })();
  const tick = async () => {
    if (token !== routeToken) return;
    let v;
    try { v = await api("GET", `/tasks/${taskId}?offset=${offset}`); }
    catch (ex) { pre.insertAdjacentHTML("beforeend", `<span class="err">${esc(ex.message)}</span>\n`); return; }
    if (token !== routeToken) return;
    const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
    pre.insertAdjacentHTML("beforeend", v.lines.map(l => {
      const cls = l.startsWith("ERROR:") ? "err" : l.startsWith("==>") || l.startsWith("Ready:") ? "step" : "";
      return cls ? `<span class="${cls}">${esc(l)}</span>\n` : esc(l) + "\n";
    }).join(""));
    if (atBottom) pre.scrollTop = pre.scrollHeight;
    offset = v.offset;
    if (v.done) { done(v); return; }
    setTimeout(tick, 1000);
  };
  tick();
}

// The sign-in choices used for setup and repair. The password is used once
// and never saved.
function signInFields(pubkey) {
  return `<div class="choice" role="radiogroup" aria-label="How to sign in for setup">
      <label class="check"><input type="radio" name="method" value="root" checked><span><b>root, with its password</b></span></label>
      <label class="check"><input type="radio" name="method" value="sudo"><span><b>Another user who can use sudo</b><br><span class="hint">For machines where root can't sign in over SSH.</span></span></label>
      <label class="check"><input type="radio" name="method" value="key"><span><b>root, with this server's key</b><br><span class="hint">If you've already added the key below to root's <span class="mono">~/.ssh/authorized_keys</span>.</span></span></label>
    </div>
    <div class="formgrid">
      <label class="field" data-show="sudo"><span>Username</span><input type="text" name="user" autocomplete="off" placeholder="admin"></label>
      <label class="field" data-show="root sudo"><span>Password</span><input type="password" name="password" autocomplete="new-password">
        <small>Used once to set the client up, then forgotten. It's never saved.</small></label>
      <div class="field full" data-show="key"><span>This server's public key</span><div class="keybox">${esc(pubkey)}</div></div>
    </div>`;
}
function wireSignIn(form) {
  const sync = () => {
    const m = form.method.value;
    $$("[data-show]", form).forEach(el => el.classList.toggle("hidden", !el.dataset.show.split(" ").includes(m)));
  };
  $$("[name=method]", form).forEach(r => r.addEventListener("change", sync));
  sync();
  return () => {
    const m = form.method.value;
    return {user: m === "sudo" ? form.user.value : "root", password: m === "key" ? "" : form.password.value, use_key: m === "key"};
  };
}
function hostKeyCheck(p, current) {
  return `<p class="m-0">${current ? "The client now shows this SSH host key" : "The client's SSH host key fingerprint is"}:</p>
    <div class="fp">${esc(p.fingerprint)}</div>
    <p class="hint m-0">Check it's really the client before going on. On the client, run <span class="mono">ssh-keygen -lf ${esc(hostKeyFile(p.type))}</span> and compare. A mismatch means you might be connecting to the wrong machine.</p>
    <label class="check mt-12"><input type="checkbox" name="matches"><span>The fingerprint matches</span></label>`;
}

async function viewClientNew(token) {
  const ssh = await api("GET", "/settings/ssh");
  if (token !== routeToken) return;
  const st = {name: "", address: "", port: 22, probe: null};
  const steps = ["Address", "Host key", "Sign in", "Set up"];
  const frame = (n, body) => render(`<a class="back" href="#/clients">‹ Clients</a><h1>Add a client</h1>
    <p class="lede">The server signs in once as root (or a sudo user) to install the backup client if needed and create a limited <span class="mono">pbcm</span> account. After that it only uses that account.</p>
    <ol class="wizard-steps">${steps.map((s, i) => `<li class="${i === n ? "on" : i < n ? "done" : ""}">${i + 1}. ${s}</li>`).join("")}</ol>
    <div class="panel">${body}</div>`);

  const step1 = () => {
    frame(0, `<form id="cf" novalidate><div class="formgrid">
        <label class="field"><span>Address</span><input type="text" name="address" value="${esc(st.address)}" placeholder="nas.lan or 192.0.2.20" autocomplete="off"><small>A host name or IP address this server can reach: on your network or over a VPN.</small></label>
        <label class="field"><span>SSH port</span><input type="number" name="port" value="${esc(st.port)}" min="1" max="65535"></label>
        <label class="field"><span>Name</span><input type="text" name="name" value="${esc(st.name)}" placeholder="Same as the address" maxlength="64"><small>How it's shown here, like “NAS” or “Web server”.</small></label>
      </div><div id="c-err"></div>
      <div class="formfoot"><button class="btn primary" type="submit">Next: check its host key</button></div></form>`);
    const f = $("#cf");
    f.address.focus();
    f.addEventListener("submit", async e => {
      e.preventDefault();
      Object.assign(st, {address: f.address.value.trim(), port: Number(f.port.value) || 22, name: f.name.value.trim()});
      const btn = $("button[type=submit]", f); btn.disabled = true; $("#c-err").innerHTML = `<div class="result info">Connecting…</div>`;
      try { st.probe = await api("POST", "/clients/probe", {address: st.address, port: st.port}); step2(); }
      catch (ex) { $("#c-err").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; btn.disabled = false; }
    });
  };
  const step2 = () => {
    frame(1, `<form id="cf" novalidate>${hostKeyCheck(st.probe)}<div id="c-err"></div>
      <div class="formfoot"><div class="btnrow"><button class="btn primary" type="submit">Next: sign in</button><button class="btn" type="button" id="c-back">Back</button></div></div></form>`);
    const f = $("#cf");
    $("#c-back").onclick = step1;
    f.addEventListener("submit", e => {
      e.preventDefault();
      if (!f.matches.checked) { $("#c-err").innerHTML = `<div class="result bad">Compare the fingerprint with the client first, then tick the box.</div>`; return; }
      step3();
    });
  };
  const step3 = () => {
    frame(2, `<form id="cf" novalidate><p class="m-0">How should the server sign in to <b>${esc(st.address)}</b> for setup?</p>${signInFields(ssh.public_key)}<div id="c-err"></div>
      <div class="formfoot"><div class="btnrow"><button class="btn primary" type="submit">Set up the client</button><button class="btn" type="button" id="c-back">Back</button></div></div></form>`);
    const f = $("#cf");
    const read = wireSignIn(f);
    $("#c-back").onclick = step2;
    f.addEventListener("submit", async e => {
      e.preventDefault();
      const btn = $("button[type=submit]", f); btn.disabled = true; $("#c-err").innerHTML = "";
      try {
        const r = await api("POST", "/clients", {name: st.name, address: st.address, port: st.port, host_key: st.probe.host_key, login: read()});
        f.password.value = "";
        step4(r.client, r.task);
      } catch (ex) { $("#c-err").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; btn.disabled = false; }
    });
  };
  const step4 = (client, taskId) => {
    frame(3, `<h2>Setting up ${esc(client.name)}</h2><p class="hint m-0">This can take a few minutes if the backup client has to be installed.</p><div id="c-log"></div><div id="c-done"></div>`);
    followTask(taskId, $("#c-log"), token, v => {
      $("#c-done").innerHTML = v.ok
        ? `<div class="result ok">${esc(client.name)} is ready.</div><div class="btnrow mt-12"><a class="btn primary" href="#/clients/${esc(client.id)}">Open ${esc(client.name)}</a><a class="btn" href="#/clients/new">Add another client</a></div>`
        : `<div class="result bad">${esc(v.error || "Setup didn't finish.")}</div><div class="btnrow mt-12"><a class="btn primary" href="#/clients/${esc(client.id)}">Open ${esc(client.name)} to repair it</a></div>`;
    });
  };
  step1();
}

async function viewClient(id, token) {
  const [{client: c, task}, ssh] = await Promise.all([api("GET", `/clients/${id}`), api("GET", "/settings/ssh")]);
  if (token !== routeToken) return;
  const banner = {
    error: `<div class="banner bad"><b>This client needs attention.</b> ${esc(c.status_detail)}</div>`,
    unreachable: `<div class="banner warn"><b>The server can't reach this client right now.</b> ${esc(c.status_detail)} Backups on the client keep running on their own schedule.</div>`,
    "host-key-changed": `<div class="banner bad"><b>The client's SSH host key has changed.</b> The server won't connect until you decide.
      <dl class="kv mt-12"><dt>Trusted key</dt><dd class="mono">${esc(c.host_key_fingerprint)}</dd><dt>Key it shows now</dt><dd class="mono">${esc(c.offered_fingerprint)}</dd></dl>
      <p class="m-0 mt-12">If you reinstalled the client or its SSH server, use <b>Repair</b> to trust the new key. Otherwise, find out why before trusting it.</p></div>`,
  }[c.status] || "";
  const pending = c.settings_pending ? `<div class="banner warn"><b>${esc(c.name)} doesn't have the latest job settings yet.</b> ${c.apply_error ? esc(c.apply_error) + ". " : ""}They're sent automatically when it can be reached.
    <div class="btnrow"><button class="btn small" id="c-apply">Send them now</button></div></div>` : "";
  const {jobs} = await api("GET", `/jobs?client=${id}`);
  if (token !== routeToken) return;
  render(`<a class="back" href="#/clients">‹ Clients</a>
    <div class="pagehead"><div><h1>${esc(c.name)} ${clientPill(c.status)}</h1><p class="lede">${esc(c.os_pretty || c.address)}</p></div>
      <div class="btnrow"><button class="btn" id="c-check">Check now</button><button class="btn" id="c-browse" ${c.status === "ready" ? "" : "disabled"}>Browse folders</button></div></div>
    ${banner}${pending}
    <div class="cols"><div class="stack">
      <div class="panel" id="c-task" ${task && !task.done ? "" : "hidden"}><h2>Setup</h2></div>
      <div class="panel"><div class="pagehead m-0"><h2 class="m-0">Backup jobs</h2><a class="btn small" href="#/jobs/new/${esc(id)}">Add a backup job</a></div>
        ${jobs.length ? `<ul class="steps mt-12">${jobs.map(j => `<li><a class="jobname" href="#/jobs/${esc(j.id)}">${esc(j.name)}</a> <span class="sub">${esc(schedText(j.schedule))}${j.enabled ? "" : ", disabled"}</span></li>`).join("")}</ul>` : `<p class="muted mt-12">No jobs for this client yet.</p>`}</div>
      <div class="panel"><h2>Repair</h2><p class="hint m-0 mb-14">Runs setup again as root: reinstalls pbcm-runner, the pbcm account and its sudo rule, and the backup client if it's missing. Use it after reinstalling the client, if its host key changed, or if something was removed by hand.</p>
        <button class="btn" id="c-repair">Repair ${esc(c.name)}</button><div id="c-repair-flow"></div></div>
      <div class="panel"><h2>Remove</h2><p class="hint m-0 mb-14">Stops managing this client.</p>
        <button class="btn danger" id="c-remove">Remove ${esc(c.name)}</button><div id="c-remove-flow"></div></div>
    </div>
    <div class="panel"><h2>Details</h2><dl class="kv">
      <dt>Address</dt><dd class="mono">${esc(c.address)} port ${esc(c.port)}</dd>
      <dt>Host name</dt><dd>${esc(c.hostname || "—")}</dd>
      <dt>System</dt><dd>${esc(c.os_pretty || "—")}${c.arch ? `, ${esc(c.arch)}` : ""}</dd>
      <dt>systemd</dt><dd>${esc(c.systemd_version || "—")}</dd>
      <dt>Time zone</dt><dd>${esc(c.timezone || "Unknown (schedules shown in this server's time)")}</dd>
      <dt>Backup client</dt><dd>${c.client_version ? `proxmox-backup-client ${esc(c.client_version)}` : `<span class="muted">Not found</span>`}</dd>
      <dt>pbcm-runner</dt><dd>${esc(c.runner_version || "—")}</dd>
      <dt>Last contact</dt><dd>${esc(ago(c.last_contact))}</dd>
      <dt>SSH host key</dt><dd class="mono break">${esc(c.host_key_fingerprint)}</dd>
      ${c.server_here ? `<dt>Note</dt><dd>This server runs on this machine too.</dd>` : ""}
    </dl></div></div>`);

  if (task && !task.done) {
    followTask(task.id, $("#c-task"), token, () => setTimeout(() => token === routeToken && route(), 800));
  }
  $("#c-check").onclick = async e => {
    e.target.disabled = true;
    try {
      const r = await api("POST", `/clients/${id}/check`);
      if (r.error) toast(r.error, "bad"); else toast(`${c.name} is reachable.`);
      route();
    } catch (ex) { toast(ex.message, "bad"); e.target.disabled = false; }
  };
  $("#c-browse").onclick = () => pickFolder(id, "/", false);
  if ($("#c-apply")) $("#c-apply").onclick = async e => {
    e.target.disabled = true;
    try { await api("POST", `/clients/${id}/apply`); toast("Settings sent."); route(); }
    catch (ex) { toast(ex.message, "bad"); e.target.disabled = false; }
  };

  $("#c-repair").onclick = async () => {
    $("#c-repair").classList.add("hidden");
    const box = $("#c-repair-flow");
    box.innerHTML = `<div class="subform"><div class="result info">Checking the client's host key…</div></div>`;
    let probe;
    try { probe = await api("POST", "/clients/probe", {address: c.address, port: c.port}); }
    catch (ex) { box.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; $("#c-repair").classList.remove("hidden"); return; }
    const changed = probe.fingerprint !== c.host_key_fingerprint;
    box.innerHTML = `<form class="subform" id="rf" novalidate>
      ${changed ? hostKeyCheck(probe, true) : `<p class="m-0 ok-text">The host key is the one already trusted.</p>`}
      ${signInFields(ssh.public_key)}<div id="r-err"></div>
      <div class="btnrow mt-16"><button class="btn primary" type="submit">Run setup again</button><button class="btn" type="button" id="r-cancel">Cancel</button></div></form>`;
    const f = $("#rf"), read = wireSignIn(f);
    $("#r-cancel").onclick = () => route();
    f.addEventListener("submit", async e => {
      e.preventDefault();
      if (changed && !f.matches.checked) { $("#r-err").innerHTML = `<div class="result bad">Compare the new fingerprint with the client first, then tick the box.</div>`; return; }
      const btn = $("button[type=submit]", f); btn.disabled = true;
      try {
        const r = await api("POST", `/clients/${id}/repair`, {login: read(), host_key: changed ? probe.host_key : ""});
        f.password.value = "";
        box.innerHTML = `<div class="subform"><div id="r-log"></div><div id="r-done"></div></div>`;
        followTask(r.task, $("#r-log"), token, v => {
          $("#r-done").innerHTML = v.ok ? `<div class="result ok">Repaired.</div>` : `<div class="result bad">${esc(v.error || "Repair didn't finish.")}</div>`;
          setTimeout(() => token === routeToken && v.ok && route(), 1500);
        });
      } catch (ex) { $("#r-err").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; btn.disabled = false; }
    });
  };

  $("#c-remove").onclick = () => {
    $("#c-remove").classList.add("hidden");
    $("#c-remove-flow").innerHTML = `<form class="subform" id="xf" novalidate>
      <div class="choice">
        <label class="check"><input type="radio" name="how" value="uninstall" checked><span><b>Remove everything this server put on the client</b><br>
          <span class="hint">The pbcm account, its sudo rule, pbcm-runner and the client's settings. proxmox-backup-client stays installed, and backups already on PBS aren't touched.</span></span></label>
        <label class="check"><input type="radio" name="how" value="list"><span><b>Only remove it from this list</b><br><span class="hint">For a client that's gone or can't be reached. Anything on it stays as it is.</span></span></label>
      </div>
      <label class="check"><input type="checkbox" name="keep" checked><span>Keep its run history on the client</span></label>
      <div id="x-err"></div>
      <div class="btnrow mt-16"><button class="btn danger" type="submit">Remove ${esc(c.name)}</button><button class="btn" type="button" id="x-cancel">Cancel</button></div></form>`;
    const f = $("#xf");
    $("#x-cancel").onclick = () => route();
    f.addEventListener("submit", async e => {
      e.preventDefault();
      const btn = $("button[type=submit]", f); btn.disabled = true; $("#x-err").innerHTML = "";
      try {
        await api("POST", `/clients/${id}/remove`, {uninstall: f.how.value === "uninstall", keep_history: f.keep.checked});
        toast(`${c.name} removed.`);
        location.hash = "#/clients";
      } catch (ex) { $("#x-err").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; btn.disabled = false; }
    });
  };
}

/* ---------- alerts ---------- */
const ALERT_KIND = {failed: "Backup failed", succeeded: "Backup succeeded", missed: "Backup didn't run", unreachable: "Can't reach client", reachable: "Client back",
  full: "Destination nearly full", space_ok: "Destination has room"};

async function viewAlerts(token) {
  const [{settings: a, password_set}, {alerts}] = await Promise.all([api("GET", "/alerts/settings"), api("GET", "/alerts?limit=50")]);
  if (token !== routeToken) return;
  const check = (name, label, hint) => `<label class="check"><input type="checkbox" name="${name}" ${a[name] ? "checked" : ""}><span><b>${label}</b>${hint ? `<br><span class="hint">${hint}</span>` : ""}</span></label>`;
  render(`<h1>Alerts</h1><p class="lede">Get an email when something needs attention. Clients keep backing up whether or not the server can reach them; alerts tell you when they don't, or when a backup fails.</p>
    ${a.enabled ? "" : `<div class="banner warn">Email alerts are off, so a failed or missed backup won't notify anyone.</div>`}
    <form id="aform" class="panel" novalidate>
      <fieldset class="section"><legend>When to send</legend>
        <div class="choice">
          ${check("enabled", "Send email alerts")}
          ${check("on_failure", "When a backup fails", "Including backups interrupted by a restart, with the reason and the end of the log.")}
          ${check("on_missed", "When a scheduled backup doesn't run", "Checked once the server has heard from the client after the scheduled time.")}
          ${check("on_unreachable", "When a client can't be reached", "And again when it's back.")}
          ${check("on_full", "When a destination is nearly full", "Before backups to it start failing. And again once there's room.")}
          ${check("on_success", "When a backup succeeds", "Usually more email than you want; failures and missed backups are the ones to watch.")}
        </div>
        <div class="formgrid top">
          <div class="field"><label for="a-grace"><span>Call a backup missed after</span></label><div class="unit"><input type="number" id="a-grace" name="missed_grace_minutes" value="${esc(a.missed_grace_minutes)}" min="10" max="1440"><span class="muted small">minutes</span></div>
            <small>How long past its scheduled time a backup may start before it counts as missed.</small></div>
          <div class="field"><label for="a-unreach"><span>Report a client after</span></label><div class="unit"><input type="number" id="a-unreach" name="unreachable_minutes" value="${esc(a.unreachable_minutes)}" min="5" max="10080"><span class="muted small">minutes unreachable</span></div>
            <small>Short network blips don't send email.</small></div>
          <div class="field"><label for="a-full"><span>Call a destination nearly full at</span></label><div class="unit"><input type="number" id="a-full" name="full_percent" value="${esc(a.full_percent)}" min="50" max="99"><span class="muted small">% used</span></div>
            <small>Space is checked every 15 minutes by default (see Settings).</small></div>
        </div>
      </fieldset>
      <fieldset class="section"><legend>Recipients</legend>
        <div class="formgrid top">
          <label class="field"><span>Send to</span><input type="text" name="to" value="${esc(a.to)}" placeholder="you@example.com"><small>Separate several addresses with commas.</small></label>
          <label class="field"><span>Send from</span><input type="email" name="from" value="${esc(a.from)}" placeholder="pbc-manager@example.com"></label>
        </div>
      </fieldset>
      <fieldset class="section"><legend>Mail server</legend>
        <p class="hint">For Gmail use smtp.gmail.com, port 587, STARTTLS and an app password rather than your normal password.</p>
        <div class="formgrid">
          <label class="field"><span>SMTP server</span><input type="text" name="host" value="${esc(a.host)}" placeholder="smtp.example.com"></label>
          <div class="formgrid">
            <label class="field"><span>Port</span><input type="number" name="port" value="${esc(a.port)}" min="1" max="65535"></label>
            <label class="field"><span>Security</span><select name="security">${[["starttls", "STARTTLS"], ["ssl", "SSL/TLS"], ["none", "None"]].map(([k, l]) => `<option value="${k}" ${a.security === k ? "selected" : ""}>${l}</option>`).join("")}</select></label>
          </div>
          <label class="field"><span>Username</span><input type="text" name="username" value="${esc(a.username)}" autocomplete="off"><small>Leave blank if the server doesn't need a sign-in.</small></label>
          <label class="field"><span>Password</span><input type="password" name="password" autocomplete="new-password" placeholder="${password_set ? "Saved. Leave blank to keep it" : ""}"><small>Stored encrypted, never shown again.</small></label>
        </div>
      </fieldset>
      <div id="a-result"></div>
      <div class="formfoot"><div class="btnrow"><button class="btn primary" type="submit">Save alert settings</button><button type="button" class="btn" id="a-test">Send a test email</button></div></div>
    </form>
    <div class="panel"><h2>Recent alerts</h2>
      ${alerts.length ? `<div class="tablewrap"><table><thead><tr><th>When</th><th>Alert</th><th>Email</th></tr></thead><tbody>
        ${alerts.map(x => `<tr><td class="small">${esc(fmtTime(x.created_at))}</td><td>${esc(x.subject.replace("[PBC Manager] ", ""))}<div class="sub">${esc(ALERT_KIND[x.kind] || x.kind)}</div></td>
          <td class="small">${x.sent_at ? `<span class="pill ok">Sent</span>` : x.error ? `<span class="pill bad">Not sent</span><div class="sub bad-text">${esc(x.error)}</div>` : `<span class="pill busy">Sending</span>`}</td></tr>`).join("")}
      </tbody></table></div>` : `<p class="muted">No alerts yet.</p>`}</div>`);
  const f = $("#aform"), out = $("#a-result");
  const read = () => ({enabled: f.enabled.checked, on_failure: f.on_failure.checked, on_success: f.on_success.checked, on_missed: f.on_missed.checked,
    on_unreachable: f.on_unreachable.checked, on_full: f.on_full.checked, full_percent: Number(f.full_percent.value) || 0, missed_grace_minutes: Number(f.missed_grace_minutes.value) || 0, unreachable_minutes: Number(f.unreachable_minutes.value) || 0,
    to: f.to.value, from: f.from.value, host: f.host.value, port: Number(f.port.value) || 0, security: f.security.value, username: f.username.value, password: f.password.value});
  f.security.addEventListener("change", () => { if ([25, 465, 587].includes(+f.port.value)) f.port.value = {starttls: 587, ssl: 465, none: 25}[f.security.value]; });
  f.addEventListener("submit", async e => {
    e.preventDefault(); out.innerHTML = "";
    const btn = $("button[type=submit]", f); btn.disabled = true;
    try { await api("PUT", "/alerts/settings", read()); f.password.value = ""; toast("Alert settings saved."); route(); }
    catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { btn.disabled = false; }
  });
  $("#a-test").addEventListener("click", async e => {
    e.target.disabled = true; out.innerHTML = `<div class="result info">Sending…</div>`;
    try { await api("POST", "/alerts/test", read()); out.innerHTML = `<div class="result ok">Test email sent. Check the inbox (and spam folder). Remember to save your settings.</div>`; }
    catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { e.target.disabled = false; }
  });
}

/* ---------- updates ---------- */
// updateBanner tells the Dashboard about a new version or a rolled-back update.
function updateBanner(u) {
  if (!u) return "";
  const l = u.last;
  if (l && l.phase === "rolled_back" && !l.seen && !l.manual)
    return `<div class="banner bad"><b>The update to ${esc(l.from)} was undone.</b> ${esc(l.reason)} Version ${esc(l.to)} is running. <a href="#/updates">Details</a></div>`;
  if (u.newer && u.check.latest)
    return `<div class="banner info">Version ${esc(u.check.latest.version)} is available. <a href="#/updates">See what's new</a></div>`;
  return "";
}

// notesHtml shows release notes (Markdown from the changelog) as plain text,
// with headings bold and list markers as bullets.
function notesHtml(text) {
  if (!text) return `<p class="muted">No release notes.</p>`;
  const lines = esc(text).split("\n").map(l => /^#{1,6}\s/.test(l) ? `<b>${l.replace(/^#+\s*/, "")}</b>` : l.replace(/^(\s*)[-*] /, "$1• "));
  return `<div class="notes">${lines.join("\n")}</div>`;
}

// waitForRestart follows the server through a restart, then reloads.
function waitForRestart(box, target) {
  const started = Date.now();
  let down = false;
  const tick = async () => {
    let v = null;
    try { const r = await fetch("api/health", {cache: "no-store"}); if (r.ok) v = (await r.json()).version; } catch (_) {}
    if (v === null) down = true;
    if (v && (v === target || down)) { location.hash = "#/updates"; location.reload(); return; }
    if (Date.now() - started > 180000) {
      box.innerHTML = `<div class="result bad">The server hasn't come back after 3 minutes. Check it with: journalctl -u pbcm -n 50</div>`;
      return;
    }
    setTimeout(tick, 2000);
  };
  setTimeout(tick, 1500);
}

async function viewUpdates(token) {
  const u = await api("GET", "/update");
  if (token !== routeToken) return;
  const c = u.check || {}, l = u.last;
  let lastHtml = "";
  if (l && !l.seen) {
    if (l.phase === "rolled_back") lastHtml = `<div class="banner ${l.manual ? "warn" : "bad"}"><b>${l.manual ? `Went back to version ${esc(l.to)}.` : `The update to ${esc(l.from)} was undone.`}</b> ${esc(l.reason)}
      <div class="btnrow"><button class="btn small" id="u-dismiss">OK</button></div></div>`;
    else if (l.phase === "done") lastHtml = `<div class="banner ok">Updated from ${esc(l.from)} to ${esc(l.to)}. <button class="btn small" id="u-dismiss">OK</button></div>`;
    else if (l.phase === "installed" || l.phase === "confirming") lastHtml = `<div class="banner info">Version ${esc(l.to)} was just installed and is being checked. If it doesn't stay up, version ${esc(l.from)} is put back automatically.</div>`;
  }
  const latest = c.latest;
  let checkHtml;
  if (c.error) checkHtml = `<div class="result bad">${esc(c.error)}</div>`;
  else if (!c.checked) checkHtml = `<p class="muted">Not checked yet.</p>`;
  else if (u.newer) checkHtml = `<h3>Version ${esc(latest.version)} is available</h3>
    <p class="sub">Released ${esc(fmtDate(latest.published))}${latest.page ? ` · <a href="${esc(latest.page)}" target="_blank" rel="noopener noreferrer">On GitHub</a>` : ""}</p>
    ${notesHtml(latest.notes)}
    <div class="btnrow mt-12"><button class="btn primary" id="u-download" ${u.cant_update ? "disabled" : ""}>Download and install ${esc(latest.version)}</button></div>`;
  else checkHtml = `<p>This is the newest version.</p>`;
  const staged = u.staged;
  const stagedHtml = staged ? `<div class="panel"><h2>Ready to install: version ${esc(staged.version)}</h2>
      <p class="hint">Checked: signed by the PBC Manager project, every file intact.</p>${notesHtml(staged.notes)}
      <div class="btnrow mt-12"><button class="btn primary" id="u-install" ${u.cant_update ? "disabled" : ""}>Install ${esc(staged.version)}</button><button class="btn" id="u-discard">Remove</button></div></div>` : "";
  const runnerRows = u.clients.length ? `<div class="tablewrap"><table><thead><tr><th>Client</th><th>pbcm-runner</th><th>Status</th></tr></thead><tbody>
      ${u.clients.map(x => `<tr><td><a href="#/clients/${esc(x.id)}">${esc(x.name)}</a></td><td class="mono small">${esc(x.runner_version || "—")}</td>
        <td><span class="pill ${{current: "ok", updating: "busy", outdated: "warn", repair: "warn"}[x.runner_state] || "idle"}">${esc(x.runner_label)}</span></td></tr>`).join("")}
    </tbody></table></div>
    ${u.clients.some(x => x.runner_state === "repair") ? `<p class="hint">${u.signed ? "These clients have a pbcm-runner too old to update itself. Use Repair on each one once." : "This server isn't a signed release (a development build), so it can't send pbcm-runner to clients. Use Repair on a client to update it."}</p>` : ""}` : `<p class="muted">No clients yet.</p>`;
  render(`<h1>Updates</h1><p class="lede">New versions install in place and keep every setting. The server restarts for a few seconds; backups on clients carry on meanwhile.</p>
    ${lastHtml}
    ${u.cant_update ? `<div class="banner warn">${esc(u.cant_update)}</div>` : ""}
    <div class="panel"><div class="pagehead m-0"><div><h2>Version ${esc(u.version)}</h2>
      <p class="sub">${c.checked ? `Checked ${esc(ago(c.checked))}` : "Never checked"}. <a href="#/settings">Daily checks and automatic updates</a> are in Settings.</p></div>
      <button class="btn" id="u-check">Check now</button></div>
      <div id="u-check-result" class="mt-12">${checkHtml}</div><div id="u-progress"></div></div>
    ${stagedHtml}
    <div class="panel"><h2>Install from a file</h2>
      <p class="hint">For a server without internet access: download <span class="mono">pbcm-&lt;version&gt;-linux-${esc(u.arch || "amd64")}.tar.gz</span> from the project's GitHub releases page and choose it here. Only files signed by the project are accepted.</p>
      <div class="btnrow"><input type="file" id="u-file" accept=".tar.gz,.tgz,application/gzip"><button class="btn" id="u-upload" disabled>Upload and check</button></div>
      <div id="u-upload-result" class="mt-12"></div></div>
    ${u.rollback_to ? `<div class="panel"><h2>Go back to version ${esc(u.rollback_to)}</h2>
      <p class="hint">Puts back the previous version and the database as it was just before the update. Changes made since then (jobs, settings, run history collected by the server) are lost; clients keep their own run history.</p>
      <button class="btn danger" id="u-rollback">Go back to ${esc(u.rollback_to)}</button></div>` : ""}
    <div class="panel"><h2>Clients</h2><p class="hint m-0 mb-14">After the server updates, it sends the matching pbcm-runner to each client the next time it checks in. Each client checks the signature before replacing anything.</p>${runnerRows}</div>`);

  const progress = $("#u-progress");
  const install = async () => {
    progress.innerHTML = `<div class="result info mt-12">Installing. The server restarts in a moment…</div>`;
    try { const r = await api("POST", "/update/install"); waitForRestart(progress, r.version); }
    catch (ex) { progress.innerHTML = `<div class="result bad mt-12">${esc(ex.message)}</div>`; }
  };
  $("#u-check").onclick = async e => {
    e.target.disabled = true;
    try { await api("POST", "/update/check"); route(); }
    catch (ex) { $("#u-check-result").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; e.target.disabled = false; }
  };
  const dl = $("#u-download");
  if (dl) dl.onclick = async () => {
    if (!confirm(`Install version ${latest.version}? The server restarts for a few seconds.`)) return;
    dl.disabled = true;
    progress.innerHTML = `<div class="result info mt-12">Downloading and checking version ${esc(latest.version)}…</div>`;
    try { await api("POST", "/update/download", {version: latest.version}); await install(); }
    catch (ex) { progress.innerHTML = `<div class="result bad mt-12">${esc(ex.message)}</div>`; dl.disabled = false; }
  };
  const inst = $("#u-install");
  if (inst) inst.onclick = () => { if (confirm(`Install version ${staged.version}? The server restarts for a few seconds.`)) { inst.disabled = true; install(); } };
  const disc = $("#u-discard");
  if (disc) disc.onclick = async () => { await api("POST", "/update/discard"); route(); };
  const dis = $("#u-dismiss");
  if (dis) dis.onclick = async () => { await api("POST", "/update/dismiss"); route(); };
  const file = $("#u-file"), up = $("#u-upload"), out = $("#u-upload-result");
  file.onchange = () => { up.disabled = !file.files.length; };
  up.onclick = async () => {
    up.disabled = true; out.innerHTML = `<div class="result info">Uploading and checking ${esc(file.files[0].name)}…</div>`;
    try {
      const r = await api("POST", "/update/upload", file.files[0]);
      if (!r.newer) { out.innerHTML = `<div class="result warn">That's version ${esc(r.staged.version)}, which isn't newer than the running ${esc(u.version)}, so it can't be installed.</div>`; await api("POST", "/update/discard"); return; }
      route();
    } catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; up.disabled = false; }
  };
  const rb = $("#u-rollback");
  if (rb) rb.onclick = async () => {
    if (!confirm(`Go back to version ${u.rollback_to}? Changes made since the update are lost.`)) return;
    rb.disabled = true;
    const box = rb.parentElement;
    try { const r = await api("POST", "/update/rollback"); box.insertAdjacentHTML("beforeend", `<div class="result info mt-12">Going back. The server restarts in a moment…</div>`); waitForRestart(box, r.version); }
    catch (ex) { toast(ex.message, "bad"); rb.disabled = false; }
  };
}

/* ---------- folder picker ---------- */
function pickFolder(clientId, start, choosing) {
  const dlg = $("#picker");
  let current = start;
  $("#picker-choose").classList.toggle("hidden", !choosing);
  $("#picker-cancel").textContent = choosing ? "Cancel" : "Close";
  return new Promise(resolve => {
    const icon = `<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.5 3.5h5l1.5 1.5h6.5v8h-13z" fill="none" stroke="currentColor" stroke-width="1.3"/></svg>`;
    const load = async path => {
      $("#picker-list").innerHTML = `<li class="loading">Loading…</li>`;
      try {
        const d = await api("GET", `/clients/${clientId}/browse?path=${encodeURIComponent(path)}`);
        current = d.path;
        $("#picker-path").textContent = d.path;
        $("#picker-list").innerHTML = (d.parent ? `<li><button type="button" data-p="${esc(d.parent)}">${icon}<span>Up one level</span></button></li>` : "") +
          (d.dirs.length ? d.dirs.map(n => `<li><button type="button" data-p="${esc((d.path === "/" ? "" : d.path) + "/" + n)}">${icon}<span class="mono">${esc(n)}</span></button></li>`).join("")
            : `<li class="muted small loading">No folders inside this one.</li>`) +
          (d.more ? `<li class="muted small loading">Only the first 1000 folders are shown.</li>` : "");
        $$("#picker-list [data-p]").forEach(b => b.addEventListener("click", () => load(b.dataset.p)));
      } catch (ex) { $("#picker-list").innerHTML = `<li class="result bad">${esc(ex.message)}</li>`; }
    };
    const done = v => { dlg.close(); resolve(v); };
    $("#picker-choose").onclick = () => done(current);
    $("#picker-cancel").onclick = () => done(null);
    dlg.onclose = () => resolve(null);
    dlg.showModal();
    load(start);
  });
}

/* ---------- settings ---------- */
async function viewSettings(token) {
  const [s, n, ssh] = await Promise.all([api("GET", "/settings"), api("GET", "/settings/network"), api("GET", "/settings/ssh")]);
  if (token !== routeToken) return;
  const field = d => {
    const v = s.values[d.key], id = "set-" + d.key.replace(/\W/g, "-");
    const help = d.help ? `<small>${esc(d.help)}</small>` : "";
    if (d.type === "bool") return `<label class="check full"><input type="checkbox" name="${esc(d.key)}" ${v ? "checked" : ""}><span><b>${esc(d.label)}</b><br><span class="hint">${esc(d.help || "")}</span></span></label>`;
    if (d.type === "int") return `<div class="field"><label for="${id}"><span>${esc(d.label)}</span></label>
      <div class="unit"><input type="number" id="${id}" name="${esc(d.key)}" value="${esc(v)}" min="${d.min}" max="${d.max}"><span class="muted small">${esc(d.unit || "")}</span></div>${help}</div>`;
    const ph = d.key === "general.server_name" ? s.hostname : d.placeholder;
    return `<label class="field"><span>${esc(d.label)}</span><input type="text" name="${esc(d.key)}" value="${esc(v)}" placeholder="${esc(ph || "")}" maxlength="${d.max_len || 200}">${help}</label>`;
  };
  render(`<h1>Settings</h1><p class="lede">Everything about how this server runs. Changes apply straight away.</p>
    <form id="sform" class="panel" novalidate>
      ${s.groups.map(g => `<fieldset class="section"><legend>${esc(g.label)}</legend>
        <div class="formgrid top">${s.defs.filter(d => d.group === g.key).map(field).join("")}</div></fieldset>`).join("")}
      <div id="s-result"></div>
      <div class="formfoot"><button class="btn primary" type="submit">Save settings</button></div>
    </form>
    <div class="panel" id="netpanel"></div>
    <div class="panel"><h2>SSH</h2><p class="hint m-0 mb-14">The key this server signs in to clients with. Setup adds it to each client's pbcm account automatically; you only need it here if you'd rather add it for root by hand before adding a client.</p>
      <dl class="kv"><dt>Public key</dt><dd><div class="keybox">${esc(ssh.public_key)}</div><button class="btn small mt-12" id="copy-key">Copy</button></dd>
      <dt>Fingerprint</dt><dd class="mono">${esc(ssh.fingerprint)}</dd></dl></div>
    <div class="panel"><h2>Export and import</h2>
      <p class="hint m-0 mb-14">Download this server's destinations, jobs, alert settings and Settings page values, or import them on another server. You can also import settings from PBS Backup Manager 1.x. Exports never include passwords or token secrets.</p>
      <div class="btnrow"><button class="btn" id="exp-btn">Download settings</button>
        <label class="btn" for="imp-file">Import a settings file…</label><input type="file" id="imp-file" class="visually-hidden" accept=".json,application/json"></div>
      <div id="imp-box"></div></div>
    <div class="panel"><h2>About</h2><dl class="kv">
      <dt>Version</dt><dd>${esc(session.version)}</dd>
      <dt>Project</dt><dd><a href="https://github.com/bradyloveland/pbcmanager" rel="noopener noreferrer" target="_blank">github.com/bradyloveland/pbcmanager</a></dd>
      <dt>Updates</dt><dd><a href="#/updates">Check for updates</a></dd>
    </dl></div>`);

  $("#copy-key").onclick = async () => {
    try { await navigator.clipboard.writeText(ssh.public_key); toast("Public key copied."); }
    catch (_) { toast("Couldn't copy. Select the key and copy it by hand.", "bad"); }
  };
  const form = $("#sform");
  form.addEventListener("submit", async e => {
    e.preventDefault();
    const values = {};
    for (const d of s.defs) {
      const el = form.elements[d.key];
      values[d.key] = d.type === "bool" ? el.checked : d.type === "int" ? Number(el.value) : el.value;
    }
    const btn = $("button[type=submit]", form); btn.disabled = true; $("#s-result").innerHTML = "";
    try {
      await api("PUT", "/settings", {values});
      await loadSession();
      $("#server-name").textContent = session.server_name;
      toast("Settings saved.");
    } catch (ex) { $("#s-result").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { btn.disabled = false; }
  });
  drawNetwork(n, token);
  wireImport(token);
}

/* ---------- export and import ---------- */
const IMPORT_KIND = {"1.x-export": "a PBS Backup Manager 1.x settings export", "1.x-config": "a PBS Backup Manager 1.x config.json",
  "pbcm-export": "a PBC Manager settings export"};

function wireImport(token) {
  $("#exp-btn").onclick = async e => {
    e.target.disabled = true;
    try {
      const res = await fetch("api/settings/export", {credentials: "same-origin"});
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `The server returned ${res.status}.`);
      const name = (res.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/)?.[1] || "pbcm-settings.json";
      const url = URL.createObjectURL(await res.blob());
      const a = document.createElement("a");
      a.href = url; a.download = name; document.body.append(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (ex) { toast(ex.message, "bad"); }
    finally { e.target.disabled = false; }
  };
  const input = $("#imp-file"), box = $("#imp-box");
  input.onchange = async () => {
    const f = input.files[0];
    input.value = "";
    if (!f) return;
    let file;
    try { file = JSON.parse(await f.text()); }
    catch (_) { box.innerHTML = `<div class="result bad mt-12">That file isn't a settings file (it isn't JSON).</div>`; return; }
    const {clients} = await api("GET", "/clients");
    if (token !== routeToken) return;
    startImport(box, file, f.name, clients, token);
  };
}

function startImport(box, file, fileName, clients, token) {
  const opts = {client_id: clients.length === 1 ? clients[0].id : "", secrets: {}, keyfile_passwords: {}, alert_password: "",
    import_alerts: true, import_settings: true, enable_schedules: false};
  const read = () => {
    const f = $("#imp-form", box);
    if (!f) return;
    if (f.client_id) opts.client_id = f.client_id.value;
    $$("[data-secret]", f).forEach(i => { opts.secrets[i.dataset.secret] = i.value; });
    $$("[data-keypw]", f).forEach(i => { opts.keyfile_passwords[i.dataset.keypw] = i.value; });
    if (f.alert_password) opts.alert_password = f.alert_password.value;
    for (const k of ["import_alerts", "import_settings", "enable_schedules"]) if (f[k]) opts[k] = f[k].checked;
  };
  const draw = (p, msg) => {
    const kind = IMPORT_KIND[p.kind] || "a settings file";
    const from = [p.version && `version ${p.version}`, p.from && `from ${p.from}`].filter(Boolean).join(", ");
    const destRows = p.destinations.map(d => `<li><b>${esc(d.name)}</b> <span class="sub mono">${esc(d.repository)}</span>
      ${d.existing ? `<div class="sub">Already here as “${esc(d.existing)}”; that one is used.</div>`
        : d.secret_included ? `<div class="sub ok-text">Token secret included.</div>`
        : `<label class="field mt-8"><span>Token secret (or password)</span><input type="password" autocomplete="off" data-secret="${esc(d.key)}" value="${esc(opts.secrets[d.key] || "")}"></label>`}</li>`).join("");
    const jobRows = p.jobs.map(j => `<li><b>${esc(j.name)}</b>${j.name !== j.old_name ? ` <span class="sub">(renamed from “${esc(j.old_name)}”, which is taken)</span>` : ""}
      <span class="sub">${j.client ? `on ${esc(j.client)} · ` : ""}${plural(j.folders, "folder")}</span>
      ${j.skip ? `<div class="sub warn-text">Not imported: ${esc(j.skip)}</div>` : ""}
      ${!j.skip && j.keyfile_password === "needed" ? `<label class="field mt-8"><span>Password for its encryption key file (if it has one)</span><input type="password" autocomplete="off" data-keypw="${esc(j.key)}" value="${esc(opts.keyfile_passwords[j.key] || "")}"></label>` : ""}</li>`).join("");
    box.innerHTML = `<form id="imp-form" class="importbox mt-16" novalidate>
      <h3>Importing ${esc(fileName)}</h3>
      <p class="sub">This is ${esc(kind)}${from ? ` (${esc(from)})` : ""}.${p.kind === "1.x-config" ? " It holds passwords; it's only read, and not kept on this server." : ""}</p>
      ${p.needs_client ? `<label class="field mt-12"><span>Which client do these backups belong to?</span>
        <select name="client_id"><option value="">Choose the machine 1.x ran on…</option>${clients.map(c => `<option value="${esc(c.id)}" ${c.id === opts.client_id ? "selected" : ""}>${esc(c.name)} (${esc(c.address)})</option>`).join("")}</select>
        <small>1.x backed up one machine. Add it as a client first if it isn't listed.</small></label>` : ""}
      ${p.destinations.length ? `<h4 class="mt-16">Destinations</h4><ul class="implist">${destRows}</ul>` : ""}
      ${p.jobs.length ? `<h4 class="mt-16">Backup jobs</h4><ul class="implist">${jobRows}</ul>` : `<p class="muted">No backup jobs in this file.</p>`}
      <div class="choice">
        ${p.alerts ? `<label class="check"><input type="checkbox" name="import_alerts" ${opts.import_alerts ? "checked" : ""}><span><b>Import email alert settings</b><br><span class="hint">Mail server ${esc(p.alerts.host || "—")}, sending to ${esc(p.alerts.to || "—")}. Replaces the current alert settings.</span></span></label>
          ${p.alerts.password_needed && opts.import_alerts ? `<label class="field"><span>Mail server password</span><input type="password" name="alert_password" autocomplete="off" value="${esc(opts.alert_password)}"><small>Not in the file. Leave blank to enter it later on the Alerts page.</small></label>` : ""}` : ""}
        ${p.settings.length ? `<label class="check"><input type="checkbox" name="import_settings" ${opts.import_settings ? "checked" : ""}><span><b>Import settings</b><br><span class="hint">${esc(p.settings.join(", "))}</span></span></label>` : ""}
        ${p.jobs.some(j => !j.skip) ? `<label class="check"><input type="checkbox" name="enable_schedules" ${opts.enable_schedules ? "checked" : ""}><span><b>Enable the imported jobs now</b><br><span class="hint">Leave this off until the old server's schedules are stopped, or both will back up the same folders. You can enable each job later with the switch at the top of its page.</span></span></label>` : ""}
      </div>
      ${p.problems.length ? `<div class="result bad"><b>Before importing:</b><ul class="m-0">${p.problems.map(x => `<li>${esc(x)}</li>`).join("")}</ul></div>` : ""}
      ${msg || ""}
      <div class="btnrow mt-12"><button class="btn primary" type="submit">Import</button><button class="btn" type="button" id="imp-cancel">Cancel</button></div>
    </form>`;
    const f = $("#imp-form", box);
    f.addEventListener("change", e => { if (e.target.name === "client_id" || e.target.type === "checkbox") { read(); preview(); } });
    $("#imp-cancel", box).onclick = () => { box.innerHTML = ""; };
    f.addEventListener("submit", async e => {
      e.preventDefault(); read();
      const btn = $("button[type=submit]", f); btn.disabled = true;
      try {
        const r = await api("POST", "/settings/import", {file, ...opts});
        if (!r.imported) { draw(r.plan); return; }
        const parts = [r.result.destinations && plural(r.result.destinations, "destination"), r.result.jobs && plural(r.result.jobs, "job")].filter(Boolean);
        box.innerHTML = `<div class="result ok mt-12">Imported ${esc(parts.join(" and ") || "the settings")}.${r.result.jobs && !opts.enable_schedules ? " The jobs are disabled; enable them once the old server is stopped." : ""} <a href="#/jobs">See the jobs</a></div>`;
      } catch (ex) { draw(lastPlan, `<div class="result bad">${esc(ex.message)}</div>`); }
    });
  };
  let lastPlan = null;
  const preview = async () => {
    try {
      const r = await api("POST", "/settings/import/preview", {file, ...opts});
      if (token !== routeToken) return;
      lastPlan = r.plan;
      draw(r.plan);
    } catch (ex) { box.innerHTML = `<div class="result bad mt-12">${esc(ex.message)}</div>`; }
  };
  box.innerHTML = `<div class="result info mt-12">Reading ${esc(fileName)}…</div>`;
  preview();
}

const TLS_LABEL = {"self-signed": "HTTPS with a self-signed certificate", custom: "HTTPS with your own certificate", off: "Off: plain HTTP"};

function certHtml(c, title) {
  if (!c) return "";
  return `<div class="certbox"><h3>${esc(title)}</h3><dl class="kv">
    <dt>Issued to</dt><dd>${esc(c.subject || "—")}${c.self_signed ? "" : ` <span class="muted">by ${esc(c.issuer)}</span>`}</dd>
    <dt>Names</dt><dd class="mono">${esc((c.names || []).join(", ") || "—")}</dd>
    <dt>Expires</dt><dd>${esc(fmtDate(c.not_after))}</dd>
    <dt>SHA-256 fingerprint</dt><dd class="mono break">${esc(c.fingerprint)}</dd></dl></div>`;
}

function drawNetwork(n, token) {
  const box = $("#netpanel"), a = n.active, p = n.pending;
  const summary = `<dl class="kv">
      <dt>Listening on</dt><dd>${a.bind ? `<span class="mono">${esc(a.bind)}</span>` : "Every network interface"}, port <span class="mono">${esc(a.port)}</span></dd>
      <dt>HTTPS</dt><dd>${esc(TLS_LABEL[a.tls])}</dd>
      <dt>Base path</dt><dd>${a.base_path ? `<span class="mono">${esc(a.base_path)}/</span>` : `<span class="muted">None</span>`}</dd>
      <dt>Trusted proxies</dt><dd>${a.trusted_proxies.length ? `<span class="mono">${a.trusted_proxies.map(esc).join(", ")}</span>` : `<span class="muted">None</span>`}</dd>
      <dt>Your connection</dt><dd>${n.request.https ? "Encrypted (HTTPS)" : `<span class="bad-text">Not encrypted (HTTP)</span>`} from <span class="mono">${esc(n.request.ip)}</span></dd>
    </dl>`;
  if (p) {
    box.innerHTML = `<h2>Network and HTTPS</h2>${summary}
      <div class="banner info mt-16"><b>A change is waiting to be confirmed.</b> ${p.here ? "This page is using the new settings. Use the banner at the top to keep them." : `Open <a href="${esc(p.url)}#/confirm-network/${esc(p.token)}">${esc(p.url)}</a> to keep it.`} It's undone automatically in ${plural(p.expires_in, "second")} if nobody confirms it.</div>`;
    return;
  }
  box.innerHTML = `<h2>Network and HTTPS</h2>${summary}
    <form id="nform" class="subform" novalidate>
      <p class="hint m-0 mb-14">Changes to the address, port, HTTPS or base path are tried first. You'll be sent to the new address to confirm them. If that doesn't work, the server goes back to the current settings by itself after about two minutes.</p>
      <div class="formgrid">
        <label class="field"><span>Listen address</span><input type="text" name="bind" value="${esc(a.bind)}" class="mono" placeholder="Every interface">
          <small>Leave blank to listen everywhere. Use 127.0.0.1 if a reverse proxy runs on this machine.</small></label>
        <label class="field"><span>Port</span><input type="number" name="port" value="${esc(a.port)}" min="1" max="65535"></label>
        <div class="field full"><span>HTTPS</span>
          <div class="seg" role="radiogroup" aria-label="HTTPS">${Object.entries(TLS_LABEL).map(([k, l]) => `<label><input type="radio" name="tls" value="${k}" ${a.tls === k ? "checked" : ""}>${esc({"self-signed": "Self-signed certificate", custom: "My own certificate", off: "Off"}[k])}</label>`).join("")}</div>
          <small id="tls-hint"></small></div>
        <div class="full" id="custom-cert">
          <div class="formgrid">
            <div class="field"><label for="cert-pem"><span>Certificate (PEM)</span></label><textarea id="cert-pem" name="cert_pem" class="pem" placeholder="-----BEGIN CERTIFICATE-----"></textarea>
              <small>Include any intermediate certificates after yours. <label class="linkbtn">Load from a file…<input type="file" data-load="cert_pem" accept=".pem,.crt,.cer,.txt" hidden></label></small></div>
            <div class="field"><label for="key-pem"><span>Private key (PEM)</span></label><textarea id="key-pem" name="key_pem" class="pem" placeholder="-----BEGIN PRIVATE KEY-----"></textarea>
              <small>Kept on this server only, readable by the service alone. <label class="linkbtn">Load from a file…<input type="file" data-load="key_pem" accept=".pem,.key,.txt" hidden></label></small></div>
          </div>
          <div id="custom-current">${n.certs.custom ? certHtml(n.certs.custom, "Certificate in use now") + `<p class="hint">Leave the boxes empty to keep using it.</p>` : ""}</div>
        </div>
        <label class="field"><span>Base path</span><input type="text" name="base_path" value="${esc(a.base_path)}" class="mono" placeholder="/backups">
          <small>Only if a reverse proxy serves this UI under a sub-path. Leave blank otherwise.</small></label>
        <label class="field"><span>Trusted reverse proxies</span><textarea name="trusted_proxies" placeholder="192.0.2.10&#10;172.16.0.0/12">${esc(a.trusted_proxies.join("\n"))}</textarea>
          <small>One IP address or network per line. Their X-Forwarded-For and X-Forwarded-Proto headers are believed; nobody else's are.</small></label>
      </div>
      <div id="n-result"></div>
      <div class="formfoot"><button class="btn primary" type="submit">Apply network settings</button></div>
    </form>
    <div id="selfsigned">${n.certs.self_signed ? certHtml(n.certs.self_signed, "Self-signed certificate") +
      `<div class="btnrow mt-12"><button class="btn small" id="regen">Create a new self-signed certificate</button><span class="hint">For example after renaming the server or changing its IP address.</span></div>` : ""}</div>`;

  const f = $("#nform");
  const sync = () => {
    const mode = f.tls.value;
    $("#custom-cert").classList.toggle("hidden", mode !== "custom");
    $("#tls-hint").innerHTML = mode === "off"
      ? `<span class="bad-text">Passwords and codes will cross the network unencrypted.</span> Only choose this when a reverse proxy in front of the server provides HTTPS.`
      : mode === "custom" ? "Paste a certificate and key from your own certificate authority or a service like Let's Encrypt."
      : "Browsers warn about a self-signed certificate the first time. That's expected on a private network.";
  };
  f.addEventListener("change", sync); sync();
  $$("[data-load]", f).forEach(inp => inp.addEventListener("change", async () => {
    const file = inp.files[0]; inp.value = "";
    if (file) f.elements[inp.dataset.load].value = await file.text();
  }));
  f.addEventListener("submit", async e => {
    e.preventDefault();
    const out = $("#n-result"); out.innerHTML = "";
    const body = {bind: f.bind.value, port: Number(f.port.value), tls: f.tls.value, base_path: f.base_path.value,
      trusted_proxies: f.trusted_proxies.value.split(/[\s,]+/).filter(Boolean),
      cert_pem: f.tls.value === "custom" ? f.cert_pem.value : "", key_pem: f.tls.value === "custom" ? f.key_pem.value : ""};
    const btn = $("button[type=submit]", f); btn.disabled = true;
    try {
      const r = await api("PUT", "/settings/network", body);
      if (r.applied) { toast("Network settings saved."); }
      else { toast("Trying the new settings. Open the new address to keep them."); }
      await loadSession(); drawPendingBanner();
      if (token === routeToken) drawNetwork(await api("GET", "/settings/network"), token);
    } catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
    finally { btn.disabled = false; }
  });
  const regen = $("#regen");
  if (regen) regen.addEventListener("click", async () => {
    if (!confirm("Create a new self-signed certificate? Browsers will warn about it once, like the first time.")) return;
    regen.disabled = true;
    try { await api("POST", "/settings/network/regenerate-certificate"); toast("New certificate created."); drawNetwork(await api("GET", "/settings/network"), token); }
    catch (ex) { toast(ex.message, "bad"); regen.disabled = false; }
  });
}

async function viewConfirmNetwork(tok, token) {
  await loadSession();
  if (token !== routeToken) return;
  const p = session.network_pending;
  if (!p || p.token !== tok) {
    render(`<h1>Network settings</h1><div class="banner warn">There's no change waiting to be confirmed. It may already have been kept, or undone because it wasn't confirmed in time.</div><a class="btn" href="#/settings">Go to Settings</a>`);
    return;
  }
  if (!p.here) {
    render(`<h1>Network settings</h1><div class="banner warn">This page was opened from the old address. Open <a href="${esc(p.url)}#/confirm-network/${esc(p.token)}">${esc(p.url)}</a> instead.</div>`);
    return;
  }
  render(`<h1>Keep the new network settings?</h1>
    <p class="lede">You reached the server using the new settings, so they work. If you don't keep them, the server goes back to the previous settings in ${plural(p.expires_in, "second")}.</p>
    <div class="panel"><div class="btnrow"><button class="btn primary" data-keep>Keep these settings</button><button class="btn" data-undo>Undo</button></div>
    <p class="hint mt-12">After keeping them, bookmark this address: <span class="mono">${esc(location.origin + location.pathname)}</span></p></div>`);
  wirePendingButtons($("#view"), p);
  $("[data-keep]").addEventListener("click", () => setTimeout(() => { if (!session.network_pending) location.hash = "#/settings"; }, 600));
}

/* ---------- account ---------- */
function codesBlock(codes) {
  return `<p>Save these recovery codes somewhere safe, like a password manager. Each one signs you in once if you lose your phone. <b>They won't be shown again.</b></p>
    <div class="codes">${codes.map(c => `<span>${esc(c)}</span>`).join("")}</div>
    <div class="btnrow"><button type="button" class="btn small" id="codes-copy">Copy codes</button><button type="button" class="btn small" id="codes-dl">Download as a text file</button><button type="button" class="btn primary small" id="codes-done">I've saved them</button></div>`;
}
function bindCodes(codes, done) {
  const text = `PBC Manager recovery codes for ${session.server_name}\nEach code can be used once.\n\n${codes.join("\n")}\n`;
  $("#codes-copy").onclick = async () => { try { await navigator.clipboard.writeText(text); toast("Recovery codes copied."); } catch (_) { toast("Couldn't copy. Select the codes and copy them by hand.", "bad"); } };
  $("#codes-dl").onclick = () => {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], {type: "text/plain"}));
    a.download = `pbcm-recovery-codes-${session.server_name}.txt`;
    document.body.append(a); a.click(); a.remove();
  };
  $("#codes-done").onclick = done;
}

async function viewAccount(token) {
  let acct = await api("GET", "/account");
  if (token !== routeToken) return;
  render(`<h1>Account</h1><p class="lede">Signed in as <b id="acct-user">${esc(acct.username)}</b> on ${esc(session.server_name)}.</p>
    <div class="panel" id="twofa"></div>
    <form id="uform" class="panel" novalidate>
      <h2>Username</h2>
      <div class="formgrid">
        <label class="field"><span>New username</span><input type="text" name="username" value="${esc(acct.username)}" autocomplete="username"></label>
        <label class="field"><span>Current password</span><input type="password" name="password" autocomplete="current-password"></label>
      </div>
      <div id="u-result"></div>
      <div class="formfoot"><button class="btn primary" type="submit">Change username</button></div>
    </form>
    <form id="pform" class="panel" novalidate>
      <h2>Password</h2>
      <div class="formgrid">
        <label class="field full w-narrow"><span>Current password</span><input type="password" name="current" autocomplete="current-password"></label>
        <label class="field"><span>New password</span><input type="password" name="new" autocomplete="new-password"><small>At least 10 characters.</small></label>
        <label class="field"><span>Repeat new password</span><input type="password" name="again" autocomplete="new-password"></label>
      </div>
      <div id="p-result"></div>
      <div class="formfoot"><button class="btn primary" type="submit">Change password</button></div>
    </form>
    <div class="panel"><h2>Locked out?</h2><p class="hint m-0 mb-14">These commands run on the server itself, for when the web UI can't help.</p><dl class="kv">
      <dt>Forgot the password</dt><dd class="mono">sudo pbcm passwd</dd>
      <dt>Lost the authenticator</dt><dd class="mono">sudo pbcm totp-reset</dd>
      <dt>Can't reach the web UI</dt><dd><span class="mono">sudo pbcm network --reset</span> <span class="muted">(every interface, port 8099, self-signed HTTPS)</span></dd>
      <dt>Service log</dt><dd class="mono">journalctl -u pbcm</dd>
    </dl></div>`);

  const box = $("#twofa");
  const drawTwofa = a => {
    acct = a;
    if (!a.totp_enabled) {
      box.innerHTML = `<h2>Two-step verification</h2>
        <div class="status-line"><span class="dot"></span><span><b>Off.</b> Signing in only needs your password.</span></div>
        <p class="hint m-0 mb-14">Turn this on to also require a 6-digit code from an authenticator app such as Aegis, 2FAS, Google Authenticator, Microsoft Authenticator, 1Password or Bitwarden.</p>
        <button class="btn primary" id="tf-start">Set up two-step verification</button><div id="tf-flow"></div>`;
      $("#tf-start").onclick = () => {
        $("#tf-start").classList.add("hidden");
        $("#tf-flow").innerHTML = `<form class="subform" id="tf-pw" novalidate>
          <label class="field w-narrow"><span>Confirm your password to continue</span><input type="password" name="password" autocomplete="current-password"></label>
          <div class="tf-err"></div>
          <div class="btnrow mt-12"><button class="btn primary" type="submit">Continue</button><button class="btn" type="button" id="tf-cancel">Cancel</button></div></form>`;
        $("#tf-cancel").onclick = () => drawTwofa(acct);
        $("#tf-pw [name=password]").focus();
        $("#tf-pw").onsubmit = async e => {
          e.preventDefault();
          try { showEnroll(await api("POST", "/account/totp/setup", {password: e.target.password.value})); }
          catch (ex) { $(".tf-err", e.target).innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
        };
      };
    } else {
      box.innerHTML = `<h2>Two-step verification</h2>
        <div class="status-line on"><span class="dot"></span><span><b>On.</b> Signing in needs your password and a code from your authenticator app.</span></div>
        <p class="hint m-0 mb-14">${plural(a.recovery_left, "recovery code")} left.${a.recovery_left <= 3 ? " Create new ones soon so you don't get locked out." : ""}</p>
        <div class="btnrow"><button class="btn" data-act="recovery">Create new recovery codes</button><button class="btn danger" data-act="disable">Turn off</button></div>
        <div id="tf-flow"></div>`;
      $$("[data-act]", box).forEach(b => b.onclick = () => {
        const disable = b.dataset.act === "disable";
        $("#tf-flow").innerHTML = `<form class="subform" novalidate>
          <p class="hint m-0 mb-14">${disable ? "Turning this off means only your password protects this server and every client's backups." : "Your existing recovery codes will stop working."}</p>
          <div class="formgrid">
            <label class="field"><span>Current password</span><input type="password" name="password" autocomplete="current-password"></label>
            <label class="field"><span>Authenticator or recovery code</span><input type="text" name="code" inputmode="numeric" autocomplete="one-time-code" class="mono"></label>
          </div>
          <div class="tf-err"></div>
          <div class="btnrow mt-16"><button class="btn ${disable ? "danger" : "primary"}" type="submit">${disable ? "Turn off two-step verification" : "Create new codes"}</button><button class="btn" type="button" id="tf-cancel">Cancel</button></div></form>`;
        const f = $("#tf-flow form");
        $("#tf-cancel").onclick = () => drawTwofa(acct);
        f.password.focus();
        f.onsubmit = async e => {
          e.preventDefault();
          try {
            const r = await api("POST", `/account/totp/${disable ? "disable" : "recovery"}`, {password: f.password.value, code: f.code.value});
            if (disable) { toast("Two-step verification is off."); drawTwofa(r); }
            else { $("#tf-flow").innerHTML = `<div class="subform">${codesBlock(r.recovery_codes)}</div>`; bindCodes(r.recovery_codes, () => drawTwofa(r)); }
          } catch (ex) { $(".tf-err", f).innerHTML = `<div class="result bad mt-12">${esc(ex.message)}</div>`; }
        };
      });
    }
  };
  const showEnroll = d => {
    $("#tf-flow").innerHTML = `<div class="subform enroll">
      <div class="qrbox">${d.qr_svg}</div>
      <form id="tf-verify" novalidate>
        <ol class="steps">
          <li>In your authenticator app, add an account and scan this QR code.</li>
          <li>Can't scan it? Enter this key instead (time-based, 6 digits):<div class="secret">${esc(d.secret)}</div></li>
          <li>Type the 6-digit code the app shows to confirm it's working.</li>
        </ol>
        <label class="field w-code"><span>Code from the app</span><input type="text" name="code" inputmode="numeric" autocomplete="one-time-code" maxlength="7" class="code-input"></label>
        <div class="tf-err"></div>
        <div class="btnrow mt-16"><button class="btn primary" type="submit">Turn on two-step verification</button><button class="btn" type="button" id="tf-cancel">Cancel</button></div>
      </form></div>`;
    $("#tf-cancel").onclick = () => drawTwofa(acct);
    const f = $("#tf-verify");
    f.code.focus();
    f.onsubmit = async e => {
      e.preventDefault();
      try {
        const r = await api("POST", "/account/totp/enable", {code: f.code.value});
        box.innerHTML = `<h2>Two-step verification</h2>
          <div class="status-line on"><span class="dot"></span><span><b>On.</b> Other signed-in browsers were signed out.</span></div>
          <div class="subform">${codesBlock(r.recovery_codes)}</div>`;
        bindCodes(r.recovery_codes, () => drawTwofa(r));
      } catch (ex) { $(".tf-err", f).innerHTML = `<div class="result bad mt-12">${esc(ex.message)}</div>`; f.code.select(); }
    };
  };
  drawTwofa(acct);

  const uf = $("#uform");
  uf.addEventListener("submit", async e => {
    e.preventDefault(); $("#u-result").innerHTML = "";
    try {
      const r = await api("PUT", "/account/username", {username: uf.username.value, password: uf.password.value});
      uf.password.value = ""; session.user = r.username; $("#acct-user").textContent = r.username;
      $("#u-result").innerHTML = `<div class="result ok">Username changed. Use “${esc(r.username)}” next time you sign in.</div>`;
    } catch (ex) { $("#u-result").innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
  });
  const pf = $("#pform");
  pf.addEventListener("submit", async e => {
    e.preventDefault(); const out = $("#p-result"); out.innerHTML = "";
    if (pf.new.value !== pf.again.value) { out.innerHTML = `<div class="result bad">The new passwords don't match.</div>`; return; }
    try { await api("POST", "/account/password", {current: pf.current.value, new: pf.new.value}); pf.reset(); out.innerHTML = `<div class="result ok">Password changed. Other signed-in browsers were signed out.</div>`; }
    catch (ex) { out.innerHTML = `<div class="result bad">${esc(ex.message)}</div>`; }
  });
}

/* ---------- start ---------- */
document.addEventListener("DOMContentLoaded", async () => {
  wireGate();
  try {
    await loadSession();
  } catch (ex) {
    const f = $("#fatal");
    f.className = "banner bad";
    f.textContent = `Can't reach the PBC Manager service: ${ex.message}`;
    return;
  }
  if (session.setup_needed) showGate("setup");
  else if (session.user) showApp();
  else showGate("login");
});
