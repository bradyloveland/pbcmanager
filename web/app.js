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
  const opts = {method, headers: {"X-PBCWM": "1"}, credentials: "same-origin"};
  if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
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
    $("#gate-title").textContent = "Set up PBC Web Manager";
    $("#gate-sub").textContent = name ? `Finish setting up the server on ${name}.` : "";
    $("#setup-form").classList.remove("hidden");
    setTimeout(() => $("#setup-form [name=code]").focus(), 0);
  } else {
    $("#gate-title").textContent = "PBC Web Manager";
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
    : "Open your authenticator app and enter the 6-digit code for PBC Web Manager.";
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
  document.title = `${session.server_name} · PBC Web Manager`;
  drawPendingBanner();
  route();
}

function render(html) { $("#view").innerHTML = html; }

const routes = [
  [/^#\/overview$/, "overview", viewOverview],
  [/^#\/clients$/, "clients", viewClients],
  [/^#\/clients\/new$/, "clients", viewClientNew],
  [/^#\/clients\/(\w+)$/, "clients", viewClient],
  [/^#\/settings$/, "settings", viewSettings],
  [/^#\/account$/, "account", viewAccount],
  [/^#\/confirm-network\/([\w-]+)$/, "settings", viewConfirmNetwork],
];

async function route() {
  if (!session || !session.user) return;
  const token = ++routeToken;
  const hash = location.hash || "#/overview";
  const match = routes.find(([re]) => re.test(hash));
  if (!match) { location.hash = "#/overview"; return; }
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

/* ---------- overview ---------- */
async function viewOverview(token) {
  const {clients} = await api("GET", "/clients");
  if (token !== routeToken) return;
  if (!clients.length) {
    render(`<div class="health"><span class="dot"></span><h1>No clients yet</h1></div>
      <div class="panel empty"><h2>Add your first client</h2>
        <p>A client is a Linux machine whose folders you want to back up. The server connects to it over SSH once to set it up; after that the client backs up on its own.</p>
        <div class="btnrow"><a class="btn primary" href="#/clients/new">Add a client</a><a class="btn" href="#/account">Turn on two-step verification</a></div></div>`);
    return;
  }
  const trouble = clients.filter(c => c.status !== "ready" && c.status !== "setting-up");
  const head = trouble.length ? (trouble.length === 1 ? `${trouble[0].name} needs attention` : `${trouble.length} clients need attention`)
    : clients.length === 1 ? "Your client is ready" : `All ${clients.length} clients are ready`;
  render(`<div class="health ${trouble.length ? "bad" : "ok"}"><span class="dot"></span><h1>${esc(head)}</h1></div>
    <p class="lede">Backup jobs and their results arrive in the next development milestone.</p>
    ${clientsTable(clients)}`);
  bindRowLinks();
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
    <p class="lede">The server signs in once as root (or a sudo user) to install the backup client if needed and create a limited <span class="mono">pbcwm</span> account. After that it only uses that account.</p>
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
  render(`<a class="back" href="#/clients">‹ Clients</a>
    <div class="pagehead"><div><h1>${esc(c.name)} ${clientPill(c.status)}</h1><p class="lede">${esc(c.os_pretty || c.address)}</p></div>
      <div class="btnrow"><button class="btn" id="c-check">Check now</button><button class="btn" id="c-browse" ${c.status === "ready" ? "" : "disabled"}>Browse folders</button></div></div>
    ${banner}
    <div class="cols"><div class="stack">
      <div class="panel" id="c-task" ${task && !task.done ? "" : "hidden"}><h2>Setup</h2></div>
      <div class="panel"><h2>Repair</h2><p class="hint m-0 mb-14">Runs setup again as root: reinstalls pbcwm-runner, the pbcwm account and its sudo rule, and the backup client if it's missing. Use it after reinstalling the client, if its host key changed, or if something was removed by hand.</p>
        <button class="btn" id="c-repair">Repair ${esc(c.name)}</button><div id="c-repair-flow"></div></div>
      <div class="panel"><h2>Remove</h2><p class="hint m-0 mb-14">Stops managing this client.</p>
        <button class="btn danger" id="c-remove">Remove ${esc(c.name)}</button><div id="c-remove-flow"></div></div>
    </div>
    <div class="panel"><h2>Details</h2><dl class="kv">
      <dt>Address</dt><dd class="mono">${esc(c.address)} port ${esc(c.port)}</dd>
      <dt>Host name</dt><dd>${esc(c.hostname || "—")}</dd>
      <dt>System</dt><dd>${esc(c.os_pretty || "—")}${c.arch ? `, ${esc(c.arch)}` : ""}</dd>
      <dt>systemd</dt><dd>${esc(c.systemd_version || "—")}</dd>
      <dt>Backup client</dt><dd>${c.client_version ? `proxmox-backup-client ${esc(c.client_version)}` : `<span class="muted">Not found</span>`}</dd>
      <dt>pbcwm-runner</dt><dd>${esc(c.runner_version || "—")}</dd>
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
  $("#c-browse").onclick = () => pickFolder(id, "/srv", false);

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
          <span class="hint">The pbcwm account, its sudo rule, pbcwm-runner and the client's settings. proxmox-backup-client stays installed, and backups already on PBS aren't touched.</span></span></label>
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
    <div class="panel"><h2>SSH</h2><p class="hint m-0 mb-14">The key this server signs in to clients with. Setup adds it to each client's pbcwm account automatically; you only need it here if you'd rather add it for root by hand before adding a client.</p>
      <dl class="kv"><dt>Public key</dt><dd><div class="keybox">${esc(ssh.public_key)}</div><button class="btn small mt-12" id="copy-key">Copy</button></dd>
      <dt>Fingerprint</dt><dd class="mono">${esc(ssh.fingerprint)}</dd></dl></div>
    <div class="panel"><h2>About</h2><dl class="kv">
      <dt>Version</dt><dd>${esc(session.version)}</dd>
      <dt>Project</dt><dd><a href="https://github.com/bradyloveland/proxmoxbackupclientwebmanager" rel="noopener noreferrer" target="_blank">github.com/bradyloveland/proxmoxbackupclientwebmanager</a></dd>
      <dt>Updates</dt><dd class="muted">Updating from this page arrives in a later development milestone.</dd>
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
  const text = `PBC Web Manager recovery codes for ${session.server_name}\nEach code can be used once.\n\n${codes.join("\n")}\n`;
  $("#codes-copy").onclick = async () => { try { await navigator.clipboard.writeText(text); toast("Recovery codes copied."); } catch (_) { toast("Couldn't copy. Select the codes and copy them by hand.", "bad"); } };
  $("#codes-dl").onclick = () => {
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([text], {type: "text/plain"}));
    a.download = `pbcwm-recovery-codes-${session.server_name}.txt`;
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
      <dt>Forgot the password</dt><dd class="mono">sudo pbcwm passwd</dd>
      <dt>Lost the authenticator</dt><dd class="mono">sudo pbcwm totp-reset</dd>
      <dt>Can't reach the web UI</dt><dd><span class="mono">sudo pbcwm network --reset</span> <span class="muted">(every interface, port 8099, self-signed HTTPS)</span></dd>
      <dt>Service log</dt><dd class="mono">journalctl -u pbcwm</dd>
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
    f.textContent = `Can't reach the PBC Web Manager service: ${ex.message}`;
    return;
  }
  if (session.setup_needed) showGate("setup");
  else if (session.user) showApp();
  else showGate("login");
});
