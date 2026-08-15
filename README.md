# Codex Model Catalog

为 Codex App 增加第三方模型，同时保留 ChatGPT 订阅模型。程序只处理 Codex App Server 的 JSONL 路由，不代理模型 HTTP 请求。

支持 Codex `model_providers` 可调用的 Responses API 兼容上游。模型及思考等级由 JSON 配置声明，新增模型不需要修改 Go 代码。

## 目录

- [安装](#安装)
  - [一键安装（推荐）](#一键安装推荐)
  - [从 GitHub Release 手动安装](#从-github-release-手动安装)
  - [从源码构建](#从源码构建)
- [如何工作](#如何工作)
- [配置](#配置)
  - [使用 LLM 生成配置](#使用-llm-生成配置)
  - [1. Provider](#1-provider)
  - [2. 模型目录与路由](#2-模型目录与路由)
  - [显示隐藏模型](#显示隐藏模型)
  - [示例：OpenCode Go](#示例opencode-go)
  - [`none` 与 Non-think](#none-与-non-think)
- [检查并启动](#检查并启动)
- [查看 token 速度统计](#查看-token-速度统计)
- [文件](#文件)
- [限制](#限制)

## 安装

前提：官方 Codex App 位于 `/Applications/Codex.app`，并已完成登录。目前仅支持 macOS Apple Silicon（arm64）。

### 一键安装（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/5kyxim/codex-model-catalog/main/scripts/install.sh | sh
```

脚本会下载最新 release 的 arm64 二进制、校验 SHA-256，安装到 `~/.codex/bin/codex-model-catalog`，然后生成并签名 `~/Applications/Codex Model Launcher.app`。

需要更新时，再次运行上面的安装命令即可；脚本会下载最新 release 并覆盖现有安装。更新后请完全退出并重新打开 `Codex Model Launcher.app`。

### 从 GitHub Release 手动安装

```bash
mkdir -p ~/Downloads/codex-model-catalog && cd ~/Downloads/codex-model-catalog
curl -fsSL -O https://github.com/5kyxim/codex-model-catalog/releases/latest/download/codex-model-catalog_darwin_arm64.tar.gz
curl -fsSL -O https://github.com/5kyxim/codex-model-catalog/releases/latest/download/checksums.txt
shasum -a 256 -c checksums.txt
tar -xzf codex-model-catalog_darwin_arm64.tar.gz
mkdir -p ~/.codex/bin
cp codex-model-catalog ~/.codex/bin/
./scripts/install-macos-app.sh
```

用浏览器下载的文件会被 macOS 标记 quarantine；用上面的 `curl` 下载不会。如果已经用浏览器下载，先执行：

```bash
xattr -d com.apple.quarantine ~/.codex/bin/codex-model-catalog
```

### 从源码构建

前提：已安装 Go。

在仓库根目录执行：

```bash
mkdir -p ~/.codex/bin
go build -trimpath -o ~/.codex/bin/codex-model-catalog ./cmd/codex-model-catalog
./scripts/install-macos-app.sh
```

脚本会生成并签名 `~/Applications/Codex Model Launcher.app`。它通过 `CODEX_CLI_PATH` 启动官方 Codex App，不会替换 `/Applications/Codex.app`。

## 如何工作

```text
Codex Model Launcher.app
  |  CODEX_CLI_PATH=~/.codex/bin/codex-model-catalog
  v
Codex App
  |  JSONL (stdin/stdout)
  v
codex-model-catalog (Go wrapper)
  |  thread/start 补 modelProvider
  v
codex app-server (官方自带)
  |
  +-- openai      -> ChatGPT 订阅
  +-- opencode_go -> OpenCode Go (Responses API)
```

`codex-model-catalog` 只做 JSONL 路由，不代理模型的 HTTP 请求；`openai` 继续走 ChatGPT 订阅，第三方 provider（如 `opencode_go`）由官方 app-server 直连上游 Responses API。

## 配置

### 使用 LLM 生成配置

把仓库中的 `docs/llm.txt` 连同上游文档、模型 ID、API 地址和密钥占位符交给任意 LLM：

```text
阅读 llm.txt，帮我生成或合并 Codex Model Catalog 配置。
```

它会按当前配置结构输出 `config.toml` Provider 配置和 `model-catalog-routes.json`。生成后运行 `doctor` 校验；不要让它修改自动生成的 `model-catalog.json`。

### 1. Provider

在现有 `~/.codex/config.toml` 中新增或合并 Provider 配置：

```toml
[model_providers.provider_id]
name = "Provider Name"
base_url = "https://api.example.com/v1"
wire_api = "responses"
request_max_retries = 4
stream_max_retries = 5
stream_idle_timeout_ms = 300000
supports_websockets = false
experimental_bearer_token = "YOUR_API_KEY"
```

当前 Codex 的 `wire_api` 只支持 `responses`，上游必须兼容 Responses API。

### 2. 模型目录与路由

编辑 `~/.codex/model-catalog-routes.json`：

```json
{
  "version": 1,
  "default_provider": "openai",
  "expose_hidden_models": false,
  "models": {
    "model-id": {
      "provider": "provider_id",
      "catalog": {
        "display_name": "Model Name",
        "description": "Provider Name",
        "default_reasoning_level": "high",
        "supported_reasoning_levels": [
          {
            "effort": "high",
            "description": "Thinking"
          }
        ],
        "input_modalities": [
          "text"
        ],
        "supports_search_tool": false,
        "web_search_tool_type": "text"
      },
      "reasoning_effort_map": {
        "high": "high",
        "xhigh": "high"
      }
    }
  }
}
```

- `models` 的键就是模型 ID；不要在 `catalog` 中重复写 `slug`。
- `provider` 必须与 `config.toml` 中的 Provider ID 一致。
- `catalog` 会覆盖内置模型模板的同名字段，可声明上下文窗口、输入类型和工具能力等模型差异。
- `reasoning_effort_map` 可选，用于把 Codex 的思考等级映射为上游接受的值；不配置时原样转发。
- `default_provider` 保持为 `openai`，未配置的内置模型继续使用 ChatGPT 订阅。
- `expose_hidden_models` 可选，默认为 `false`。启用方法和限制见下一节。

不在 `config.toml` 中设置全局 `model`，Codex App 就会继续默认选择内置模型。第三方模型需要手动选择。

### 显示隐藏模型

只需把 `~/.codex/model-catalog-routes.json` 中已有的顶层字段设为 `true`，不需要逐个配置隐藏模型，也不要删除现有的 `models` 内容：

```json
"expose_hidden_models": true
```

生成目录时，wrapper 会把上游缓存中的 `visibility: "hide"` 改为 `list`。它不会修改 `supported_in_api`，也不会绕过账号、工作区或服务端权限；出现在目录中不代表模型一定可以调用。单模型 `catalog.visibility` 会在全局开关之后应用，因此仍可把指定模型设回 `hide`。

Codex App 还可能按模型 ID 做额外过滤。例如当前版本会固定排除 `codex-auto-review`，所以即使生成目录中的可见性已经是 `list`，它也不会出现在普通模型选择器。`gpt-5.6-sol-wm` 可以通过这个开关显示，但其上游目录当前声明 `supported_in_api: false`。

修改后先刷新目录：

```bash
~/.codex/bin/codex-model-catalog doctor
```

然后按 Command-Q 完全退出当前 Codex App，再打开 `~/Applications/Codex Model Launcher.app`。

### 示例：OpenCode Go

Provider：

```toml
[model_providers.opencode_go]
name = "OpenCode Go"
base_url = "https://opencode.ai/zen/go/v1"
wire_api = "responses"
supports_websockets = false
experimental_bearer_token = "YOUR_API_KEY"
```

仓库示例包含 DeepSeek V4 Flash 的完整模型条目和思考等级映射。仅在首次配置、目标文件不存在时复制：

```bash
cp examples/model-catalog-routes.example.json ~/.codex/model-catalog-routes.json
```

已有配置时请合并 `models`，不要直接覆盖。示例将 `minimal` 映射为 `low`，`medium`、`xhigh` 映射为 `high`，`ultra` 映射为 `max`；同名等级原样转发。

### `none` 与 Non-think

`none` 需要同时满足两层兼容：

1. 上游接受 Responses API 的 `reasoning.effort = "none"`。
2. Codex 客户端允许用户选择 `none`。

当前测试版本的 Codex App 会再次过滤模型目录中的思考等级，模型选择器默认不显示 `none`。例如目录声明 `none`、`low`、`high`、`max` 时，App 可能只显示 `low`、`high`、`max`。

在 `supported_reasoning_levels` 和 `reasoning_effort_map` 中配置 `none`，只能保证路由程序在收到该值时正确转发，不能强制 Codex App 显示它。不要为了绕过界面限制，擅自把 `low` 映射为 `none`；这会让界面标签与实际行为不一致。如果上游提供独立的 Non-think 模型 ID 或别名，可以把它作为单独模型配置。

## 检查并启动

`doctor` 会校验配置，并生成或刷新 `~/.codex/model-catalog.json`：

```bash
~/.codex/bin/codex-model-catalog doctor
```

检查通过后：

1. 按 Command-Q 完全退出正在运行的 Codex App。
2. 打开 `~/Applications/Codex Model Launcher.app`。
3. 在新任务的模型选择器中选择内置或第三方模型。

如果在 Finder 的“应用程序”里看不到它，可以直接用命令打开：

```bash
open ~/Applications/Codex\ Model\ Launcher.app
```

启动器继续使用 Codex 原有的任务列表，内置与第三方 Provider 的历史任务会同时显示。

## 查看 token 速度统计

启动后 wrapper 会在 `~/.codex/codex-model-catalog.sock` 提供一个本机 unix socket，
不需要任何 TCP 端口：

```bash
curl --unix-socket ~/.codex/codex-model-catalog.sock http://localhost/stats
```

默认输出按模型分组的 terminal 排行榜；请求头带 `Accept: application/json` 时返回 JSON。

排查事件链路时可以用 `/debug` 查看 wrapper 实际观察到的通知次数：

```bash
curl --unix-socket ~/.codex/codex-model-catalog.sock http://localhost/debug
```

不想起 App、也不想 curl 时，可以直接读盘查看最近 24 小时统计：

```bash
~/.codex/bin/codex-model-catalog stats
```

统计设计：

- 保留最近 24 小时、最多 10,000 个已完成回合，落盘到
  `~/.codex/codex-model-catalog-stats.jsonl`（仅 `0600` 权限），重启不丢；
- terminal 排行榜展示过去 24 小时的 token 加权平均速度：每个回合的 `tok/s`
  按该回合输出 token 数加权，输出越多的回合对结果影响越大；
- 排行榜按输出 token 总数降序，`RELATIVE SPEED` 以最快模型为基准显示相对长度，
  `TOKENS` 和 `RUNS` 分别表示参与计算的输出 token 与已完成回合数；
- JSON 保留原有的合并速率、`15m / 1h / 6h / 24h` 四档窗口和 24 个一小时的
  `token/min` sparkline，并新增 `token_weighted_tokens_per_second`；
- 日志只记录模型、token 数、回合时长和结束时间，不记录 prompt 或输出原文。

`unknown` 不是一个真实模型名，而是 wrapper 处理回合事件时没有找到对应的模型绑定。修复或升级不会回填已有记录，它们会在 24 小时窗口结束后自然消失；如果完全重启启动器后 `unknown` 仍持续增加，说明当前仍有后台或内部任务没有可用的模型绑定，需要结合 `/debug` 和运行日志继续定位，不能把它当成某个模型的速度。

## 文件

| 路径 | 用途 |
| --- | --- |
| `~/.codex/config.toml` | Provider 与可选默认模型 |
| `~/.codex/model-catalog-routes.json` | 模型目录、路由和思考等级映射 |
| `~/.codex/model-catalog.json` | `doctor` 或启动时自动生成的完整模型目录，不要手改 |
| `~/.codex/bin/codex-model-catalog` | Codex App Server 路由程序 |
| `scripts/install.sh` | 一键安装脚本：自动下载最新 release 并安装 |
| `scripts/install-macos-app.sh` | 生成本地 `~/Applications/Codex Model Launcher.app` |
| `~/.codex/codex-model-catalog.sock` | 运行时的 token 速度统计 unix socket |
| `~/.codex/codex-model-catalog-stats.jsonl` | 24 小时统计日志，仅模型/token/时长，不含原文 |
| `docs/llm.txt` | 供 LLM 生成或合并配置的规则与模板 |

## 限制

- 一个任务内不能切换 Provider；如需切换，请新建任务，或 fork 后为新任务选择目标模型。fork 不会修改原任务的 Provider。
- 修改 `model-catalog-routes.json` 不需要重新编译，但需要完全退出并重新打开 `Codex Model Launcher.app`。
- 当前测试版本的 Codex App 默认不在模型选择器中显示 `none`；模型目录和路由程序支持该值，不代表 App 界面会开放该选项。
