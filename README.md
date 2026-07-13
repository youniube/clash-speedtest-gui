# Clash-SpeedTest Windows 图形版

面向 Windows 的 Clash/Mihomo 节点测速、筛选和配置导出工具。日常使用不需要命令行，双击 `Clash-SpeedTest-GUI.exe` 即可。

## 快速开始

GUI 只需要以下三个文件放在同一目录：

- `Clash-SpeedTest-GUI.exe`
- `subscription-parser.exe`
- `speedtest-runner.exe`

`clash-speedtest.exe` 是保留的上游命令行程序，GUI 不依赖它。

1. 粘贴订阅地址、单节点链接或选择本地 YAML 文件。多个输入必须每行一个；不使用逗号分隔，因此 URL、Windows 文件名和节点参数中的逗号会原样保留。
2. 选择测速方案，第一次使用建议选择“均衡（推荐）”。
3. 点击“开始测速”。
4. 完成后可复制节点 URL、Clash Meta 或节点名称；按 `F2` 可重命名节点，按 `Delete` 可从本地结果中删除已导出的节点。列表上方还可按状态、延迟、协议和已查询的出口国家组合筛选，也可以手动查询有效节点的出口地区。

键盘可用 `F5` 开始测速、`F6` 查询尚未成功识别地区的有效节点，任务运行时按 `Esc` 停止。快捷键由窗口统一处理，即使焦点位于多行输入框或结果表格也会生效；下拉列表展开时，`Esc` 只关闭下拉列表，不会误停任务。

| 测速方案 | 用途 | 默认流量 |
| --- | --- | --- |
| 快速（仅延迟） | 快速检查节点是否可用 | 几乎不产生测速流量 |
| 均衡（推荐） | 日常筛选延迟和下载速度 | 每节点最多约 20MB |
| 深度（大流量纯下载） | 用更大样本和更严格阈值复核下载质量 | 每节点最多约 50MB |
| 自定义 | 手动控制高级参数 | 取决于设置 |

流量按节点计算。例如 40 个节点使用均衡方案，理论上最多约使用 800MB；实际用量可能因失败或超时更低。
程序会强制限制每条下载连接实际读取的字节数，即使自定义服务器忽略 `Range` 也不会继续读取完整文件。所有方案都只发送 GET 请求；带文件路径的地址直接使用 `Range` 下载，不带路径的测速服务使用 `/__down`。

测速分为两个独立的并发层级：先并发进行低流量 HTTP 探测，淘汰不可达或未通过延迟/失败率初筛的节点；下载模式再让候选节点逐个完成吞吐测试，每个当前节点内部可使用 1–16 条下载连接。节点间串行能避免它们争抢本机带宽，其他应用仍可能影响结果。每个节点探测完成后会立即显示“失败”或“等待下载”，开始传输时显示“下载中”，不再等整批探测结束后才出现第一条结果。

每个节点会先做 1 次不计入统计的预热，再做 5 次正式 GET 探测，正式样本之间间隔 100ms。直链使用 `Range: bytes=0-0`，无路径测速服务使用 `/__down?bytes=1`；延迟取成功正式样本的中位数，“HTTP 探测失败率”是 5 次正式 HTTP 请求中的失败比例，不是 ICMP/UDP 丢包率。

探测超时和下载超时互相独立。达到下载超时时，界面会保留实际字节数和墙钟时间计算出的部分速度，并标记“传输未完成”；未完成计划字节数的节点不会作为有效节点导出，不会再把已有的有效采样简单显示为 `0 MB/s`。

下载速度表示 `当前电脑网络 → 代理节点 → 当前测速服务器` 这条路径在本次测试时段的吞吐，不代表节点访问所有网站、所有地区或所有时间段的绝对带宽。

测速失败、协议输出不完整、没有节点通过，或在本地结果提交前停止时，不会覆盖已有输出文件。如果在本地结果已经原子提交后停止，已保存的本地结果会保留，后续节点同步或 Gist 上传会取消，状态栏会明确说明。完整说明见 [GUI-使用说明.md](GUI-使用说明.md)。

测速和出口地区查询期间，会锁定所有会改变任务行为的设置；“停止”会取消当前解析器、测速器、地区查询、后处理和正在等待的 Gist 请求。窗口关闭也会先等待任务停止并清理临时文件。地区查询按整批事务处理：只有协议完整且进程正常退出才应用结果；取消、半截输出或协议错误会恢复查询前的地区信息。节点事件按批次刷新列表，大批量节点不会再为每一行重复执行全表筛选和统计。

如果停止发生在 GitHub 已经接收最终 Gist 创建或更新请求之后，客户端无法回滚远端变更；程序会提示检查远端结果，而不会错误承诺 Gist 一定未变化。

主列表固定为七列：`序号、节点名称、类型、HTTP 延迟、下载速度、出口地区、状态`。快速模式的下载速度显示“未测试”；无论升序还是降序，“未测试”和无结果都会排在有效数值之后。测速时状态栏显示 `已探测 x/总数`、`等待下载 y`、`正在下载：节点名称` 以及有效、失败、等待数量。

节点名称修改和删除只作用于本地筛选结果，不会回写原订阅；启用 Gist 时会在本地保存成功后自动同步 Gist。服务器、端口或凭据不开放编辑，因为连接参数变化后旧测速结果会立即失效。旧版设置中的 `NodeNotes` 会在启动时自动永久清除。

GUI 创建的是秘密（不公开列出）Gist，并不具备严格访问控制：任何拿到链接的人都可能读取其中的节点配置。`%APPDATA%\ClashSpeedTestGUI\settings.json` 也会明文保存用户选择保留的订阅/节点输入、输出路径、测速参数、GitHub 用户名和 Token；不要分享 Gist 链接、设置文件或输出 YAML。

合并多个输入源时，同名但配置不同的节点不会被丢弃：第一个保留原名，后续节点自动使用 `原名 [2]`、`原名 [3]`。输出文件不得与任何本地输入配置或三个 GUI 运行程序相同，避免误覆盖原始配置或程序文件。

出口地区是 IP 数据库的近似定位，默认不查询。点击“查询有效节点出口地区”后，程序只查询本轮已导出的有效节点，并确保请求通过各自节点代理访问；依次回退 IPWHOIS.io、FreeIPAPI 和 IP.SB，不会同时向三家发送请求。结果只保存在当前界面内，不包含或保存出口 IP、ASN、运营商，不写入 YAML，重新测速后清空。勾选“按真实出口地区重命名节点”时，程序会在本地结果提交前执行同一套出口查询，成功节点按 `🇯🇵 日本 JP-01` 命名；单个查询失败保留原名，整批协议失败则全部保留原名，绝不根据入口 `server` 或旧名称猜测。

## 当前内核与构建

- `subscription-parser.exe` 和 `speedtest-runner.exe` 均使用 Mihomo `v1.19.27`。
- 测速层基于 `clash-speedtest v1.8.8` 做本地适配，源码位于 `tools/speedtest-runner/internal/upstream/`。
- 节点只按完整配置指纹去重，不按 `server:port` 合并，避免误删同入口但凭据或传输参数不同的节点。
- GUI 严格校验测速事件协议 v5 的版本、表头、节点数量、稳定 ID、进度顺序、固定字段和结果完整性。`probe_completed` 每节点只能出现一次；`download_started` 只能在下载模式且必须晚于探测完成。`tested` 表示下载已经启动，`complete` 表示计划传输全部完成。有效状态及排序/筛选使用内核提供的原始纳秒、字节每秒、HTTP 探测失败率和完成状态，不再从格式化文字反推。重复、乱序、未知、缺失或半截输出不会提交结果文件。
- 出口地区事件使用独立协议 v2；GUI 和查询器共同校验声明数量、稳定 ID、字段类型、成功/失败语义以及最终完整性。重复、未知、缺失或畸形事件会终止本批查询，已验证但尚未提交的结果也不会形成半更新。

升级 Mihomo 后必须同时重新构建解析器和测速器，不要单独替换其中一个 EXE：

```powershell
.\build-gui.ps1
```

发布前执行完整回归。脚本默认先从当前源码重建三个 EXE，再运行 GUI 自测、UI 夹具契约、两个 Go 模块的无缓存测试和 `go vet`，避免旧 EXE 造成假绿：

```powershell
.\test-all.ps1
```

只想复核当前目录里已有的 EXE 时，才使用：

```powershell
.\test-all.ps1 -SkipBuild
```

默认回归不会操作桌面。需要同时验证真实 WinForms 点击、测速、重命名、删除和最终截图时，执行：

```powershell
.\test-all.ps1 -IncludeWinFormsUI
```

操作级回归会打开一个隔离的测试 GUI；运行期间不要手动操作该窗口。首次执行会下载固定版本且校验 SHA-256 的 FlaUI 依赖到 `%LOCALAPPDATA%\ClashSpeedTestGUI\test-tools\FlaUI\5.0.0\`，不会安装系统组件，也不会注册快捷键或调用 PixPin。成功截图保存到 `tests\ui\artifacts\last-success.png`。

自动化不读取真实订阅、节点凭据或 Token，也不使用公开测速网址。网络算法测试使用 `127.0.0.1` 随机端口上的临时 HTTP/代理服务；WinForms 测试使用 `%TEMP%` 下的本地 YAML 和固定进程夹具，不会访问 DNS、地区服务或 Gist。

## 构建产物与校验

`v2.0.0` 是不兼容升级：测速事件协议升至 v5，并删除上传测速、`full` 模式及对应 CLI 参数。依赖旧测速器参数或旧事件协议的外部自动化需要同步调整；只通过 GUI 使用的用户会自动迁移旧设置。正式 Windows x86_64 安装包只包含三个运行 EXE、README、GUI 使用说明和 GPL-3.0 许可证。Windows EXE 当前没有代码签名，首次运行可能出现 SmartScreen 提示，这不等同于哈希校验失败。

发布者必须从干净源码构建并为三个 EXE 生成新的 SHA-256；每次重建后哈希都会变化，不能沿用旧版本。用户应把下载文件的 `Get-FileHash -Algorithm SHA256` 结果与同一 Release 公布的校验值逐个比对：

```powershell
Get-FileHash .\Clash-SpeedTest-GUI.exe, .\subscription-parser.exe, .\speedtest-runner.exe -Algorithm SHA256
```

## 本地敏感配置

真实订阅配置可能包含服务器地址、账号或密钥，不应放在源码和发布目录中。项目初始化 Git 时已忽略根目录的 `cs.yaml`、`wa.yaml`、`filtered.yaml` 和所有 EXE；原有 `cs.yaml`、`wa.yaml` 已保持内容不变并迁移到：

```text
%LOCALAPPDATA%\ClashSpeedTestGUI\private-inputs\
```

需要使用这些本地配置时，在 GUI 中通过“选择文件”打开该目录中的文件即可。`filtered.yaml` 是默认生成结果，可以继续保留在程序目录，但不会进入 Git。

## 上游命令行程序参考

以下内容介绍项目中保留的 `clash-speedtest.exe`。只使用 GUI 时可以忽略。

基于 Clash/Mihomo 核心的测速工具，快速测试你的节点速度。

Features:
1. 无需额外的配置，直接将 Clash/Mihomo 配置本地文件路径或者订阅地址作为参数传入即可
2. 支持 Proxies 和 Proxy Provider 中定义的全部类型代理节点，兼容性跟 Mihomo 一致
3. 不依赖额外的 Clash/Mihomo 进程实例，单一工具即可完成测试
4. 代码简单而且开源，不发布构建好的二进制文件，保证你的节点安全

<img width="1346" height="682" alt="Image" src="https://github.com/user-attachments/assets/9fea1d47-251f-4c49-b059-05b5962d4e72" />

## Prerequisites/注意事项

### OpenWRT 环境
在 OpenWRT 环境下使用本工具时，建议临时关闭 OpenClash/Clash/Mihomo 等代理服务，以避免路由冲突影响测速结果的准确性。或者给 OpenClash/Clash/Mihomo 配置进程规则绕过代理：
```
rules:
  - PROCESS-NAME,clash-speedtest,DIRECT
```

### Windows CMD 用户
在 Windows CMD 中使用时，如果订阅地址包含 `&` 字符，必须使用双引号而非单引号：
```bash
# 正确
> clash-speedtest -c "https://domain.com/api/v1/client/subscribe?token=secret&flag=meta"

# 错误
> clash-speedtest -c 'https://domain.com/api/v1/client/subscribe?token=secret&flag=meta'
```

## 使用方法

```bash
# 支持从源码安装，或从 Release 里下载由 Github Action 自动构建的二进制文件
> go install github.com/faceair/clash-speedtest@latest

# 查看版本
> clash-speedtest -v

# 查看帮助
> clash-speedtest -h

# 参数和默认值以当前二进制输出为准，不在 README 复制一份容易过期的帮助表。

# 演示：

# 1. 测试全部节点，使用 HTTP 订阅地址
# 请在订阅地址后面带上 flag=meta 参数，否则无法识别出节点类型
> clash-speedtest -c 'https://domain.com/api/v1/client/subscribe?token=secret&flag=meta'

# 2. 测试香港节点，使用正则表达式过滤，使用本地文件
> clash-speedtest -c ~/.config/clash/config.yaml -f 'HK|港'
节点                                        	带宽          	延迟
Premium|广港|IEPL|01                        	484.80KB/s  	815.00ms
Premium|广港|IEPL|02                        	N/A         	N/A
Premium|广港|IEPL|03                        	2.62MB/s    	333.00ms
Premium|广港|IEPL|04                        	1.46MB/s    	272.00ms
Premium|广港|IEPL|05                        	3.87MB/s    	249.00ms

# 3. 当然你也可以混合使用
> clash-speedtest -c "https://domain.com/api/v1/client/subscribe?token=secret&flag=meta,/home/.config/clash/config.yaml"

# 4. 筛选出延迟低于 800ms 且下载速度大于 5MB/s 的节点，并输出到 filtered.yaml
> clash-speedtest -c "https://domain.com/api/v1/client/subscribe?token=secret&flag=meta" -output filtered.yaml -max-latency 800ms -min-download-speed 5
# 筛选后的配置文件可以直接粘贴到 Clash/Mihomo 中使用，或是贴到 Github\Gist 上通过 Proxy Provider 引用。

# 5. 快速测试模式
> clash-speedtest -f 'HK' -fast -c ~/.config/clash/config.yaml
# 此命令将只测试节点延迟，跳过其他测试项目，适用于：
# - 快速检查节点是否可用
# - 只需要检查延迟的场景
# - 需要快速得到测试结果的场景
🇭🇰 香港 HK-10 100% |██████████████████| (20/20, 13 it/min)
序号    节点名称                类型            延迟
1.      🇭🇰 香港 HK-01           Trojan          657ms
2.      🇭🇰 香港 HK-20           Trojan          649ms
3.      🇭🇰 香港 HK-15           Trojan          674ms
4.      🇭🇰 香港 HK-19           Trojan          649ms
5.      🇭🇰 香港 HK-12           Trojan          667ms

# 6. 上传到 GitHub Gist
> clash-speedtest -c config.yaml -output result.yaml -gist-token "ghp_xxx" -gist-address "https://gist.github.com/user/abc123"
# 测试完成后，会将 result.yaml 上传到指定的 Gist，文件名与 -output 保持一致（去除目录前缀）
# gist-address 可以是完整的 Gist URL，也可以是 Gist ID（如 abc123）
# Gist/Repo 上传与远程配置 URL 加载默认遵循环境代理变量（HTTPS_PROXY/HTTP_PROXY）。

# 7. 上传到 GitHub 仓库文件（默认写入 output 文件名）
> clash-speedtest -c config.yaml -output result.yaml -repo-token "ghp_xxx" -repo-address "user/repo"
# 测试完成后，会将 result.yaml 上传到仓库默认分支下的 result.yaml

# 8. 上传到 GitHub 仓库指定分支与路径
> clash-speedtest -c config.yaml -output result.yaml -repo-token "ghp_xxx" -repo-address "https://github.com/user/repo" -repo-file-path "configs/subscriptions/result.yaml" -repo-branch "main"
```

## GitHub Token 创建与权限

### 1) 更新 Gist（`-gist-token`）

推荐使用 **Personal access tokens (classic)**：

1. 打开 GitHub `Settings` → `Developer settings` → `Personal access tokens` → `Tokens (classic)`。
2. 点击 `Generate new token (classic)`。
3. 仅勾选最小权限：`gist`。
4. 生成后复制 token，作为 `-gist-token` 传入。

最小权限结论：
- `gist`：必需（用于通过 API 更新 Gist 文件）。

### 2) 更新仓库文件（`-repo-token`）

可选两种 token：

#### A. Fine-grained PAT（推荐）

1. 打开 GitHub `Settings` → `Developer settings` → `Personal access tokens` → `Fine-grained tokens`。
2. `Repository access` 选择目标仓库（建议 `Only select repositories`）。
3. 在 `Repository permissions` 中设置：
   - `Contents`: **Read and write**（必需）
4. 生成后复制 token，作为 `-repo-token` 传入。

#### B. Tokens (classic)

- 更新**公开仓库**文件：至少 `public_repo`。
- 更新**私有仓库**文件：至少 `repo`。

最小权限结论：
- Fine-grained PAT：`Contents: Read and write`。
- Classic PAT：公有仓库 `public_repo`，私有仓库 `repo`。

### 常见权限问题

- `401 Unauthorized`：token 无效、过期，或复制时有空格/换行。
- `403 Forbidden`：token 权限不足，或目标分支启用了保护策略（可能禁止直接 push/commit）。
- `404 Not Found`：仓库地址/路径/分支不正确，或 token 对该仓库不可见。

> 安全建议：不要把 token 提交到仓库；优先通过环境变量或 CI Secret 注入。

## 测速原理

GUI 的本地测速器只使用 HTTP GET：先通过节点做一次预热和五次 1 字节正式探测，再对通过初筛的节点执行固定字节数的纯下载测试。带路径的 `server-url` 作为直接下载地址并使用 `Range`；不带路径的地址使用 `/__down?bytes=...`。下载使用多连接和 `io.Discard`，严格限制读取量并校验 `Content-Range`。

测试结果：

1. 下载速度是指定字节数除以实际墙钟时间，代表 `当前电脑网络 → 代理节点 → 当前测速服务器` 的本次路径吞吐。数值越高，说明这条路径在当时的可用吞吐越高；它不是节点在所有网站和所有时间段的绝对带宽。
2. HTTP 延迟是五次正式一字节 HTTP GET 探测中成功样本的中位耗时，包含经节点代理完成请求的应用层开销；它不是 ICMP Ping，也不是一次大文件下载的首字节时间。数值越低通常表示交互响应越快。
3. HTTP 探测失败率只统计五次正式 HTTP 请求中的失败比例，不代表网络层丢包率。

请注意带宽跟延迟是两个独立的指标，两者并不关联：
1. 可能带宽很高但是延迟也很高，这种情况下你下载速度很快但是打开网页的时候却很慢，可能是是中转节点没有 BGP 加速，但出海线路带宽很充足。
2. 可能带宽很低但是延迟也很低，这种情况下你打开网页的时候很快但是下载速度很慢，可能是中转节点有 BGP 加速，但出海线路的 IEPL、IPLC 带宽很小。

## License

[GPL-3.0](LICENSE)
