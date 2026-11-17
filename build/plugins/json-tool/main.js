// JSON 工具：toy 的 JavaScript 插件示例。
// run(action, inputs, tool) 返回以输出 id 为键的对象。

function sortKeys(value) {
  if (Array.isArray(value)) return value.map(sortKeys);
  if (value && typeof value === "object") {
    const out = {};
    for (const k of Object.keys(value).sort()) out[k] = sortKeys(value[k]);
    return out;
  }
  return value;
}

function parse(text) {
  try {
    return JSON.parse(text);
  } catch (e) {
    throw new Error("不是合法的 JSON：" + e.message);
  }
}

function run(action, inputs) {
  let value = parse(inputs.text || "");
  if (inputs.sort_keys) value = sortKeys(value);

  switch (action) {
    case "format": {
      const indent = { "2 空格": 2, "4 空格": 4, "Tab": "\t" }[inputs.indent] ?? 2;
      return { result: JSON.stringify(value, null, indent) };
    }
    case "minify": {
      const out = JSON.stringify(value);
      host.log(`压缩后 ${out.length} 个字符`);
      return { result: out };
    }
    case "table": {
      const rows = Array.isArray(value) ? value : [value];
      const columns = [];
      for (const row of rows) {
        if (row && typeof row === "object" && !Array.isArray(row)) {
          for (const k of Object.keys(row)) if (!columns.includes(k)) columns.push(k);
        }
      }
      if (columns.length === 0) throw new Error("需要对象或对象数组");
      const cell = (v) => (v !== null && typeof v === "object" ? JSON.stringify(v) : String(v ?? ""));
      return {
        result: `${rows.length} 行 × ${columns.length} 列`,
        table: { columns, rows: rows.map((r) => columns.map((c) => cell(r?.[c]))) },
      };
    }
  }
  throw new Error("未知操作: " + action);
}
