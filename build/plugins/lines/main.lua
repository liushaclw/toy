-- 文本行处理：toy 的 Lua 插件示例。
-- run(action, inputs, tool) 返回一个以输出 id 为键的表。

local function split_lines(text)
  local lines = {}
  for line in (text .. "\n"):gmatch("(.-)\r?\n") do
    lines[#lines + 1] = line
  end
  if lines[#lines] == "" then lines[#lines] = nil end
  return lines
end

local function trim(s)
  return (s:gsub("^%s+", ""):gsub("%s+$", ""))
end

function run(action, inputs)
  local lines = split_lines(inputs.text or "")
  local before = #lines

  local kept = {}
  for _, line in ipairs(lines) do
    if inputs.trim then line = trim(line) end
    if not (inputs.drop_empty and line == "") then
      kept[#kept + 1] = line
    end
  end
  lines = kept

  local function key(s)
    if inputs.ignore_case then return s:lower() end
    return s
  end

  if action == "sort" then
    table.sort(lines, function(a, b) return key(a) < key(b) end)
  elseif action == "unique" then
    local seen, unique = {}, {}
    for _, line in ipairs(lines) do
      if not seen[key(line)] then
        seen[key(line)] = true
        unique[#unique + 1] = line
      end
    end
    lines = unique
  elseif action == "reverse" then
    local n = #lines
    for i = 1, math.floor(n / 2) do
      lines[i], lines[n - i + 1] = lines[n - i + 1], lines[i]
    end
  else
    error("未知操作: " .. action)
  end

  host.log(string.format("%s: %d 行 -> %d 行", action, before, #lines))
  return {
    result = table.concat(lines, "\n"),
    summary = string.format("输入 %d 行，输出 %d 行", before, #lines),
  }
end
