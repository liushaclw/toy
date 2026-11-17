// 编解码：toy 的 JavaScript 插件。
// goja 没有 btoa/atob/TextEncoder，UTF-8 和 Base64 在这里自己实现。

// ---- UTF-8 ----

function utf8Encode(str) {
  const bytes = [];
  for (const ch of str) {
    let cp = ch.codePointAt(0);
    if (cp >= 0xd800 && cp <= 0xdfff) cp = 0xfffd; // a lone surrogate
    if (cp < 0x80) bytes.push(cp);
    else if (cp < 0x800) bytes.push(0xc0 | (cp >> 6), 0x80 | (cp & 63));
    else if (cp < 0x10000) bytes.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 63), 0x80 | (cp & 63));
    else bytes.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 63), 0x80 | ((cp >> 6) & 63), 0x80 | (cp & 63));
  }
  return bytes;
}

// utf8Decode throws when the bytes are not UTF-8 (binary data).
function utf8Decode(bytes) {
  let out = "";
  for (let i = 0; i < bytes.length; ) {
    const b = bytes[i];
    let n, cp;
    if (b < 0x80) { n = 0; cp = b; }
    else if ((b & 0xe0) === 0xc0) { n = 1; cp = b & 31; }
    else if ((b & 0xf0) === 0xe0) { n = 2; cp = b & 15; }
    else if ((b & 0xf8) === 0xf0) { n = 3; cp = b & 7; }
    else throw new Error(`第 ${i + 1} 个字节不是合法的 UTF-8`);
    for (let k = 1; k <= n; k++) {
      const c = bytes[i + k];
      if (c === undefined) throw new Error("UTF-8 序列不完整");
      if ((c & 0xc0) !== 0x80) throw new Error(`第 ${i + k + 1} 个字节不是合法的 UTF-8`);
      cp = (cp << 6) | (c & 63);
    }
    if (cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) throw new Error(`第 ${i + 1} 个字节开始的字符不合法`);
    out += String.fromCodePoint(cp);
    i += n + 1;
  }
  return out;
}

// ---- Base64 ----

const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

function base64Encode(bytes, url) {
  let out = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i] << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0);
    out += B64[(n >> 18) & 63] + B64[(n >> 12) & 63];
    out += i + 1 < bytes.length ? B64[(n >> 6) & 63] : "=";
    out += i + 2 < bytes.length ? B64[n & 63] : "=";
  }
  if (url) out = out.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  return out;
}

// base64Decode takes standard or URL-safe Base64, with or without padding and whitespace.
function base64Decode(text) {
  const s = text.replace(/\s+/g, "").replace(/-/g, "+").replace(/_/g, "/").replace(/=+$/, "");
  if (s.length % 4 === 1) throw new Error("Base64 长度不对");
  const bytes = [];
  let bits = 0, value = 0;
  for (let i = 0; i < s.length; i++) {
    const v = B64.indexOf(s[i]);
    if (v < 0) throw new Error(`第 ${i + 1} 个字符「${s[i]}」不是 Base64 字符`);
    value = (value << 6) | v;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      bytes.push((value >> bits) & 255);
    }
  }
  return bytes;
}

// ---- Hex ----

function hexEncode(bytes) {
  return bytes.map((b) => b.toString(16).padStart(2, "0")).join("");
}

function hexDecode(text) {
  const s = text.replace(/0x/gi, "").replace(/[\s:,-]+/g, "");
  if (!/^[0-9a-fA-F]*$/.test(s)) throw new Error("包含非十六进制字符");
  if (s.length % 2) throw new Error("十六进制位数必须是偶数");
  const bytes = [];
  for (let i = 0; i < s.length; i += 2) bytes.push(parseInt(s.substr(i, 2), 16));
  return bytes;
}

// ---- HTML ----

const HTML_NAMED = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " ", copy: "©", reg: "®", trade: "™", hellip: "…", mdash: "—", ndash: "–", yen: "¥", euro: "€" };

function htmlEncode(str, all) {
  let out = "";
  for (const ch of str) {
    switch (ch) {
      case "&": out += "&amp;"; continue;
      case "<": out += "&lt;"; continue;
      case ">": out += "&gt;"; continue;
      case '"': out += "&quot;"; continue;
      case "'": out += "&#39;"; continue;
    }
    const cp = ch.codePointAt(0);
    out += all && cp > 0x7e ? `&#x${cp.toString(16).toUpperCase()};` : ch;
  }
  return out;
}

function htmlDecode(str) {
  return str.replace(/&(#[xX][0-9a-fA-F]+|#\d+|[a-zA-Z]+);/g, (m, body) => {
    if (body[0] === "#") {
      const cp = body[1] === "x" || body[1] === "X" ? parseInt(body.slice(2), 16) : parseInt(body.slice(1), 10);
      return cp <= 0x10ffff ? String.fromCodePoint(cp) : m;
    }
    return HTML_NAMED[body] ?? m;
  });
}

// ---- Unicode escapes ----

function unicodeEncode(str, all) {
  let out = "";
  for (let i = 0; i < str.length; i++) {
    const u = str.charCodeAt(i); // UTF-16 units, so astral characters become surrogate pairs
    out += all || u > 0x7e ? "\\u" + u.toString(16).padStart(4, "0") : str[i];
  }
  return out;
}

function unicodeDecode(str) {
  return str
    .replace(/\\u\{([0-9a-fA-F]{1,6})\}/g, (_, h) => String.fromCodePoint(parseInt(h, 16)))
    .replace(/\\u([0-9a-fA-F]{4})/g, (_, h) => String.fromCharCode(parseInt(h, 16)))
    .replace(/\\x([0-9a-fA-F]{2})/g, (_, h) => String.fromCharCode(parseInt(h, 16)));
}

// ---- JWT ----

function jwtDecode(token) {
  const parts = token.trim().split(".");
  if (parts.length !== 3) throw new Error("JWT 应该是用 . 分开的三段");
  const part = (s, name) => {
    try {
      return JSON.parse(utf8Decode(base64Decode(s)));
    } catch (e) {
      throw new Error(`${name}无法解析：${e.message}`);
    }
  };
  const header = part(parts[0], "头部");
  const payload = part(parts[1], "载荷");
  const info = [`算法：${header.alg ?? "无"}`];
  const when = (name, label) => {
    if (typeof payload[name] === "number") info.push(`${label}：${new Date(payload[name] * 1000).toISOString()}`);
  };
  when("iat", "签发");
  when("nbf", "生效");
  when("exp", "过期");
  if (typeof payload.exp === "number") info.push(payload.exp * 1000 < Date.now() ? "已过期" : "未过期");
  return {
    result: JSON.stringify({ header, payload }, null, 2),
    info: info.join("\n"),
  };
}

// ---- entry ----

function bytesInfo(bytes) {
  return `${bytes.length} 字节`;
}

function encode(codec, text, all) {
  switch (codec) {
    case "Base64":
    case "Base64 URL": {
      const bytes = utf8Encode(text);
      return { result: base64Encode(bytes, codec === "Base64 URL"), info: bytesInfo(bytes) };
    }
    case "URL":
      return { result: encodeURIComponent(text) };
    case "Hex": {
      const bytes = utf8Encode(text);
      return { result: hexEncode(bytes), info: bytesInfo(bytes) };
    }
    case "HTML 实体":
      return { result: htmlEncode(text, all) };
    case "Unicode 转义":
      return { result: unicodeEncode(text, all) };
    case "JWT":
      throw new Error("JWT 只能解码（编码需要密钥签名）");
  }
  throw new Error("未知格式：" + codec);
}

// decodeBytes shows decoded bytes as text, or as hex when they are not UTF-8.
function decodeBytes(bytes) {
  try {
    return { result: utf8Decode(bytes), info: bytesInfo(bytes) };
  } catch (e) {
    return { result: hexEncode(bytes), info: `${bytesInfo(bytes)}，不是 UTF-8 文本，以十六进制显示` };
  }
}

function decode(codec, text) {
  switch (codec) {
    case "Base64":
    case "Base64 URL":
      return decodeBytes(base64Decode(text));
    case "URL":
      try {
        return { result: decodeURIComponent(text.replace(/\+/g, " ")) };
      } catch (e) {
        throw new Error("不是合法的 URL 编码：" + e.message);
      }
    case "Hex":
      return decodeBytes(hexDecode(text));
    case "HTML 实体":
      return { result: htmlDecode(text) };
    case "Unicode 转义":
      return { result: unicodeDecode(text) };
    case "JWT":
      return jwtDecode(text);
  }
  throw new Error("未知格式：" + codec);
}

function run(action, inputs) {
  const text = inputs.text ?? "";
  const out = action === "encode" ? encode(inputs.codec, text, inputs.all) : decode(inputs.codec, text);
  return { result: out.result, info: out.info ?? "" };
}
