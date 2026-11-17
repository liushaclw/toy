// HTTP 工具：toy 的 JavaScript 插件。
// 请求用 host.http 发送；保存的请求放在插件存储的 "requests" 里：名称 → 请求。

const STORE = "requests";

function saved() {
  return host.get(STORE) || {};
}

function names(store) {
  return Object.keys(store).sort((a, b) => a.localeCompare(b));
}

// The form's request, as it is saved and loaded.
function requestOf(inputs) {
  return {
    method: inputs.method || "GET",
    url: (inputs.url || "").trim(),
    headers: inputs.headers || "",
    body: inputs.body || "",
    timeout: inputs.timeout,
    follow: inputs.follow !== false,
  };
}

// parseHeaders reads "Name: value" lines; blank lines and # comments are skipped.
function parseHeaders(text) {
  const headers = {};
  text.split(/\r?\n/).forEach((line, i) => {
    line = line.trim();
    if (!line || line.startsWith("#")) return;
    const colon = line.indexOf(":");
    if (colon <= 0) throw new Error(`请求头第 ${i + 1} 行应该是「名称: 值」：${line}`);
    const name = line.slice(0, colon).trim();
    const value = line.slice(colon + 1).trim();
    if (headers[name] === undefined) headers[name] = value;
    else headers[name] = [].concat(headers[name], value);
  });
  return headers;
}

function hasHeader(headers, name) {
  name = name.toLowerCase();
  return Object.keys(headers).some((k) => k.toLowerCase() === name);
}

function isJSON(text) {
  const t = text.trim();
  if (!(t.startsWith("{") || t.startsWith("["))) return false;
  try {
    JSON.parse(t);
    return true;
  } catch (e) {
    return false;
  }
}

function formatSize(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function send(inputs) {
  const req = requestOf(inputs);
  if (!req.url) throw new Error("请填写 URL");
  const headers = parseHeaders(req.headers);
  if (req.body && !hasHeader(headers, "Content-Type") && isJSON(req.body)) {
    headers["Content-Type"] = "application/json";
  }
  const resp = host.http({
    method: req.method, url: req.url, headers, body: req.body,
    timeout: req.timeout || 0, redirects: req.follow,
  });

  let status = `${resp.status_text} · ${Math.round(resp.elapsed_ms)} ms · ${formatSize(resp.size)}`;
  if (resp.truncated) status += "（只读了前 32 MB）";
  const asked = /^[a-z][a-z0-9+.-]*:\/\//i.test(req.url) ? req.url : "http://" + req.url;
  if (resp.url !== asked) status += `\n→ ${resp.url}`;

  let body = resp.body;
  const type = Object.keys(resp.headers).find((k) => k.toLowerCase() === "content-type");
  if (type && /json/i.test(resp.headers[type]) || isJSON(body)) {
    try {
      body = JSON.stringify(JSON.parse(body), null, 2);
    } catch (e) {
      // not JSON after all: show it as it came
    }
  }
  const rows = Object.keys(resp.headers).sort().map((k) => [k, resp.headers[k]]);
  host.log(`${req.method} ${req.url} → ${resp.status}`);
  return { status, body, headers: { columns: ["名称", "值"], rows } };
}

function list() {
  return { $options: { saved: names(saved()) } };
}

function load(inputs) {
  const name = inputs.saved;
  const store = saved();
  const req = store[name];
  if (!req) return list(); // gone meanwhile: offer what is left
  return {
    $inputs: {
      method: req.method, url: req.url, headers: req.headers, body: req.body,
      timeout: req.timeout, follow: req.follow, name,
    },
    $status: `已载入「${name}」`,
  };
}

function save(inputs) {
  const req = requestOf(inputs);
  if (!req.url) throw new Error("请填写 URL 再保存");
  const name = (inputs.name || "").trim() || inputs.saved || `${req.method} ${req.url}`;
  const store = saved();
  const replaced = store[name] !== undefined;
  store[name] = req;
  host.set(STORE, store);
  return {
    $options: { saved: names(store) },
    $inputs: { saved: name, name },
    $status: replaced ? `已更新「${name}」` : `已保存「${name}」`,
  };
}

function remove(inputs) {
  const name = inputs.saved;
  const store = saved();
  if (!name || store[name] === undefined) throw new Error("先在「已保存的请求」里选一个");
  delete store[name];
  host.set(STORE, store);
  return { $options: { saved: names(store) }, $inputs: { name: "" }, $status: `已删除「${name}」` };
}

function run(action, inputs) {
  switch (action) {
    case "send": return send(inputs);
    case "list": return list();
    case "load": return load(inputs);
    case "save": return save(inputs);
    case "delete": return remove(inputs);
  }
  throw new Error("未知动作：" + action);
}
