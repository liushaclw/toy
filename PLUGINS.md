# 编写 toy 插件

一个插件就是 `plugins/` 下的一个目录：一个 `plugin.toml` 加上脚本或程序。保存后窗口会自动重载，
之前填的表单内容也会保留。

插件从这些目录加载，后面的目录可以覆盖前面同 id 的插件：

1. 程序所在目录下的 `plugins/`
2. 当前工作目录下的 `plugins/`（`viva dev .` 时就是项目里的 `plugins/`）
3. 用户目录 `%APPDATA%\toy\plugins\`

插件用 `get`/`set` 存的值放在 `%APPDATA%\toy\data\<插件id>.json`。

## plugin.toml

```toml
id = "lines"                 # 唯一，必填
name = "文本行处理"
version = "1.0.0"
description = "..."
runtime = "lua"              # lua | js | exec
entry = "main.lua"           # lua / js：入口脚本
# command = ["go", "run", "."]  # exec：启动命令；第一个参数如果是本目录里的文件，就从本目录运行

[[tools]]                    # 一个插件可以有多个工具，每个工具在侧栏里占一项
id = "lines"
name = "文本行处理"
description = "显示在表单上方"
timeout = 10                 # 秒；不写为 120，负数表示不限
# init = "list"             # 打开工具时自动运行的动作（不必是按钮），比如填充下拉框的选项

  [[tools.inputs]]
  id = "text"                # 插件收到的键名
  type = "textarea"          # text | password | textarea | number | switch | select | file
  label = "输入"
  placeholder = "每行一项"
  rows = 8                   # textarea 的行数
  default = "..."            # switch 写 true/false，select 写其中一个选项
  required = true            # 为空时不运行，直接提示
  # options = ["a", "b"]     # select 的选项；可以为空，由返回值的 $options 填充
  # on_change = "load"       # select、switch 被用户改动时自动运行的动作（不必是按钮）

  [[tools.actions]]          # 每个动作是一个按钮；不写时默认一个"运行"(id = "run")
  id = "sort"
  label = "排序"
  # style = "primary"        # 按钮样式：primary | secondary | outline | ghost | danger | danger-outline
                             # 不写时第一个动作是 primary（蓝色），其余是 outline
  # confirm = "确定删除「{name}」吗？"  # 先弹窗确认再运行；{输入 id} 换成该输入当前的值；
                             # 引用的输入为空时不弹窗，直接运行（由插件提示缺什么）
  # requires = ["saved"]     # 这个动作必填的输入，代替各输入的 required；空着时直接提示，不运行

  [[tools.outputs]]          # 不写时默认一个文本输出 result
  id = "result"
  type = "text"              # text | table
  label = "结果"
  copyable = true
```

插件收到的输入值：文本类是字符串，`number` 是数字，`switch` 是布尔，`select` 是选中的选项文字，
`file` 是路径字符串。

插件的返回值：

- 返回一个字符串：显示在第一个输出里。
- 返回一个以输出 id 为键的表/对象：分别显示到对应输出。
- 表格输出可以是 `{columns = {...}, rows = {{...}, ...}}`，也可以是二维数组，
  或者对象数组（对象的键作为列名）。
- 以 `$` 开头的保留键用来改表单，所有语言（包括 exec）都适用：
  - `$inputs`：`{输入 id: 值}`，把值填进表单（select 的值必须是现有选项之一，空字符串表示不选）。
  - `$options`：`{select 的 id: [选项...]}`，替换下拉框的选项；当前选中的不在新选项里就清空。
  - `$status`：一行文字，显示在按钮下方，代替"完成，用时 …"。
  - 只返回保留键、没有任何输出键时，上一次的输出保持不变（比如"保存"不会清掉刚才的响应）。

  完整的例子见 `plugins/http`：`init` 读出已保存的请求填进下拉框，`on_change` 选中后载入。

抛出错误时，错误信息显示在表单下方。

## Lua（gopher-lua，Lua 5.1）

```lua
function run(action, inputs, tool)
  host.log("开始", action)            -- 写到日志；print 也会写到日志
  host.progress(0.5)                  -- 0..1，进度条
  host.clipboard("文本")              -- 写入剪贴板
  local n = (host.get("count") or 0) + 1
  host.set("count", n)                -- 持久保存
  local body, err = host.http_get("https://example.com")
  local resp, err = host.http({method = "POST", url = "https://example.com", headers = {["X-Token"] = "t"}, body = "..."})
  local text, err = host.read_file("data.txt")   -- 相对路径以插件目录为准
  host.write_file("out.txt", text)
  return { result = "..." }
end
```

`host.http(请求)` 发送任意 HTTP 请求。请求：`method`（默认 GET）、`url`、`headers`（名称 → 值，或值的列表）、
`body`（文本）、`timeout`（秒，默认 30，0 表示只受工具的 timeout 限制）、`redirects`（默认 true，跟随重定向）。
响应：`status`（200）、`status_text`（"200 OK"）、`proto`、`url`（重定向之后的）、`headers`（名称 → 值，重复的用 ", " 连接）、
`body`（文本，最多 32 MB）、`size`、`truncated`、`elapsed_ms`。任何状态码都算正常返回，只有拿不到响应（连不上、超时）才算失败：
Lua 返回 `nil, 错误信息`，JavaScript 抛异常。

Lua 标准库全部可用（`io`、`os` 也可以），用 `require` 可以加载插件目录里的其他模块。

## JavaScript（goja，ES2015+）

```js
function run(action, inputs, tool) {
  console.log("开始", action);        // 写到日志
  host.progress(0.5);
  host.clipboard("文本");
  host.set("count", (host.get("count") ?? 0) + 1);
  const body = host.httpGet("https://example.com");   // 同步调用，出错时抛异常
  const resp = host.http({ method: "POST", url: "https://example.com", headers: { "X-Token": "t" }, body: "..." });
  const text = host.readFile("data.txt");
  host.writeFile("out.txt", text);
  return { result: "..." };
}
```

只有一个入口文件，不支持 `import`/`require`；没有事件循环，所以不要依赖 `setTimeout` 或尚未完成的 Promise。

## exec（任意语言的独立进程）

宿主启动 `command`，用 JSON-RPC 2.0 通信，标准输入/输出上每行一条 JSON 消息。
进程在第一次运行时启动，之后一直复用；进程退出后，下次运行会重新启动。
标准错误输出的每一行都会写进日志，所以**不要往标准输出打印日志**。

宿主 → 插件：

| 消息 | 参数 | 说明 |
|---|---|---|
| `initialize`（请求） | `{protocol: 1, plugin, dir}` | 返回任意结果（如 `{}`）即表示就绪 |
| `run`（请求） | `{tool, action, inputs}` | 返回值即动作的结果（格式同上） |
| `$/cancel`（通知） | `{id}` | 用户取消了请求 `id`；2 秒内没有应答就结束进程 |

插件 → 宿主：

| 消息 | 参数 |
|---|---|
| `log`（通知） | `{text}` |
| `progress`（通知） | `{fraction}` |
| `clipboard`（通知） | `{text}` |
| `get`（请求） | `{key}`，返回存的值 |
| `set`（请求） | `{key, value}` |

宿主关闭标准输入时，插件应该退出。

用 Go 写的话可以直接用 `toy/sdk/toyplugin`（只依赖标准库，可以复制到别的模块里用）：

```go
func main() {
	toyplugin.Handle("hash", func(c *toyplugin.Call) (any, error) {
		c.Log("working on %s", c.String("file"))
		c.Progress(0.5)
		if c.Context.Err() != nil { /* 用户点了取消 */ }
		return map[string]any{"result": "..."}, nil
	})
	toyplugin.Serve()
}
```

示例 `plugins/hash` 用 `go run .` 启动，方便开发（第一次运行要编译，会慢几秒）。
发布时可以先 `go build` 出 `hash.exe` 放进插件目录，再把配置改成 `command = ["hash"]`。
