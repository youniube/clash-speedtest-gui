# Clash-SpeedTest Windows 图形版

面向 Windows 的 Clash/Mihomo 节点测速、筛选和配置导出工具。日常使用不需要命令行，双击 `Clash-SpeedTest-GUI.exe` 即可。

## 快速开始

GUI 只需要以下三个文件放在同一目录：

- `Clash-SpeedTest-GUI.exe`
- `subscription-parser.exe`
- `speedtest-runner.exe`

`clash-speedtest.exe` 是保留的上游命令行程序，GUI 不依赖它。

1. 粘贴订阅地址、单节点链接、多行节点列表，或选择本地 YAML 文件。批量粘贴时每行一个节点。
2. 选择测速方案，第一次使用建议选择“均衡（推荐）”。
3. 点击“开始测速”。
4. 完成后可复制节点 URL、Clash Meta 或节点名称；按 `F2` 可重命名节点，按 `Delete` 可从本地结果中删除已导出的节点。列表上方还可按状态、延迟、协议和已查询的出口国家组合筛选，也可以手动查询有效节点的出口地区。

键盘可用 `F5` 开始测速、`F6` 查询尚未成功识别地区的有效节点，任务运行时按 `Esc` 停止。快捷键由窗口统一处理，即使焦点位于多行输入框或结果表格也会生效；下拉列表展开时，`Esc` 只关闭下拉列表，不会误停任务。

| 测速方案 | 用途 | 默认流量 |
| --- | --- | --- |
| 快速（仅延迟） | 快速检查节点是否可用 | 几乎不产生测速流量 |
| 均衡（推荐） | 日常筛选延迟和下载速度 | 每节点最多约 20MB |
| 深度（含上传） | 同时检查下载和上传质量 | 每节点最多约 70MB |
| 自定义 | 手动控制高级参数 | 取决于设置 |

流量按节点计算。例如 40 个节点使用均衡方案，理论上最多约使用 800MB；实际用量可能因失败或超时更低。
程序会强制限制每条下载连接实际读取的字节数，即使自定义服务器忽略 `Range` 也不会继续读取完整文件。深度模式必须使用不带文件路径或查询参数、且支持 `/__down` 与 `/__up` 的测速服务；默认直接下载地址只支持快速和均衡模式，程序会在开始前明确阻止不兼容的深度测试。

测速失败、协议输出不完整、没有节点通过，或在本地结果提交前停止时，不会覆盖已有输出文件。如果在本地结果已经原子提交后停止，已保存的本地结果会保留，后续节点同步或 Gist 上传会取消，状态栏会明确说明。完整说明见 [GUI-使用说明.md](GUI-使用说明.md)。

测速和出口地区查询期间，会锁定所有会改变任务行为的设置；“停止”会取消当前解析器、测速器、地区查询、后处理和正在等待的 Gist 请求。窗口关闭也会先等待任务停止并清理临时文件。地区查询按整批事务处理：只有协议完整且进程正常退出才应用结果；取消、半截输出或协议错误会恢复查询前的地区信息。节点事件按批次刷新列表，大批量节点不会再为每一行重复执行全表筛选和统计。

如果停止发生在 GitHub 已经接收最终 Gist 创建或更新请求之后，客户端无法回滚远端变更；程序会提示检查远端结果，而不会错误承诺 Gist 一定未变化。

主列表固定为八列：`序号、节点名称、类型、延迟、下载速度、上传速度、出口地区、状态`。快速模式的下载/上传、均衡模式的上传显示“未测试”；无论升序还是降序，“未测试”和无结果都会排在有效数值之后。状态栏显示总数、筛选后、已选、有效、失败和等待数量。

节点名称修改和删除只作用于本地筛选结果，不会回写原订阅；启用 Gist 时会在本地保存成功后自动同步 Gist。服务器、端口或凭据不开放编辑，因为连接参数变化后旧测速结果会立即失效。旧版设置中的 `NodeNotes` 会在启动时自动永久清除。

合并多个输入源时，同名但配置不同的节点不会被丢弃：第一个保留原名，后续节点自动使用 `原名 [2]`、`原名 [3]`。输出文件不得与任何本地输入配置或三个 GUI 运行程序相同，避免误覆盖原始配置或程序文件。

出口地区是 IP 数据库的近似定位，默认不查询。点击“查询有效节点出口地区”后，程序只查询本轮已导出的有效节点，并确保请求通过各自节点代理访问；依次回退 IPWHOIS.io、FreeIPAPI 和 IP.SB，不会同时向三家发送请求。结果只保存在当前界面内，不包含或保存出口 IP、ASN、运营商，不写入 YAML，重新测速后清空。

## 当前内核与构建

- `subscription-parser.exe` 和 `speedtest-runner.exe` 均使用 Mihomo `v1.19.27`。
- 测速层基于 `clash-speedtest v1.8.8` 做本地适配，源码位于 `tools/speedtest-runner/internal/upstream/`。
- 节点只按完整配置指纹去重，不按 `server:port` 合并，避免误删同入口但凭据或传输参数不同的节点。
- GUI 严格校验测速事件协议 v3 的版本、表头、节点数量、稳定 ID、固定字段和结果完整性；有效状态及排序/筛选使用内核提供的原始纳秒、字节每秒和丢包率指标，不再从格式化文字反推。协议不匹配或半截输出不会提交结果文件。
- 出口地区事件使用独立协议 v2；GUI 和查询器共同校验声明数量、稳定 ID、字段类型、成功/失败语义以及最终完整性。重复、未知、缺失或畸形事件会终止本批查询，已验证但尚未提交的结果也不会形成半更新。

升级 Mihomo 后必须同时重新构建解析器和测速器，不要单独替换其中一个 EXE：

```powershell
.\build-gui.ps1
```

发布前执行完整回归；脚本会运行 GUI 自测、两个 Go 模块的无缓存测试和 `go vet`：

```powershell
.\test-all.ps1
```

默认回归不会操作桌面。需要同时验证真实 WinForms 点击、测速、重命名、删除和最终截图时，执行：

```powershell
.\test-all.ps1 -IncludeWinFormsUI
```

操作级回归会打开一个隔离的测试 GUI；运行期间不要手动操作该窗口。首次执行会下载固定版本且校验 SHA-256 的 FlaUI 依赖到 `%LOCALAPPDATA%\ClashSpeedTestGUI\test-tools\FlaUI\5.0.0\`，不会安装系统组件，也不会注册快捷键或调用 PixPin。成功截图保存到 `tests\ui\artifacts\last-success.png`。

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
Usage of clash-speedtest:
  -c string
        configuration file path, also support http(s) url
  -ua string
        User-Agent for fetching config from http(s) URL (default: mihomo kernel UA, e.g. mihomo/1.10.0)
  -f string
        filter proxies by name, use regexp (default ".*")
  -b string
        block proxies by keywords, use | to separate multiple keywords (example: -b 'rate|x1|1x')
  -server-url string
        server url or direct download url (default "https://dl.google.com/chrome/mac/universal/stable/GGRO/googlechrome.dmg")
  -speed-mode string
        speed test mode: fast, download, full (default "download")
  -download-size int
        download size for testing proxies (default 50MB)
  -upload-size int
        upload size for testing proxies (full mode only) (default 20MB)
  -timeout duration
        timeout for testing proxies (default 5s)
  -concurrent int
        download concurrent size (default 4)
  -output string
        output config file path (default "")
  -max-latency duration
        filter latency greater than this value (default 800ms)
  -max-packet-loss float
        filter packet loss greater than this value(unit: %) (default 100)
  -min-download-speed float
        filter speed less than this value(unit: MB/s) (default 5)
  -min-upload-speed float
        filter upload speed less than this value(unit: MB/s, full mode only) (default 2)
  -rename
        rename nodes with IP location and speed
  -fast
        fast mode (alias for --speed-mode fast)
  -gist-token string
        GitHub personal access token for gist upload
  -gist-address string
        gist URL or ID for uploading output file (filename uses output basename)
  -repo-token string
        GitHub personal access token for repository file upload
  -repo-address string
        repository URL or owner/repo for uploading output file
  -repo-file-path string
        repository file path for uploading output file (default: output basename)
  -repo-branch string
        repository branch for uploading output file (default: repository default branch)

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

# 5. 使用 -rename 选项按照 IP 地区和下载速度重命名节点
> clash-speedtest -c config.yaml -output result.yaml -rename
# 重命名后的节点名称格式：🇺🇸 US 001 | ⬇️ 15.67MB/s
# 包含国旗 emoji、国家代码和下载速度

# 6. 快速测试模式
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

# 7. 上传到 GitHub Gist
> clash-speedtest -c config.yaml -output result.yaml -gist-token "ghp_xxx" -gist-address "https://gist.github.com/user/abc123"
# 测试完成后，会将 result.yaml 上传到指定的 Gist，文件名与 -output 保持一致（去除目录前缀）
# gist-address 可以是完整的 Gist URL，也可以是 Gist ID（如 abc123）
# Gist/Repo 上传与远程配置 URL 加载默认遵循环境代理变量（HTTPS_PROXY/HTTP_PROXY）。

# 8. 上传到 GitHub 仓库文件（默认写入 output 文件名）
> clash-speedtest -c config.yaml -output result.yaml -repo-token "ghp_xxx" -repo-address "user/repo"
# 测试完成后，会将 result.yaml 上传到仓库默认分支下的 result.yaml

# 9. 上传到 GitHub 仓库指定分支与路径
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

通过 HTTP GET 请求下载指定大小的文件，默认使用 https://dl.google.com/chrome/mac/universal/stable/GGRO/googlechrome.dmg 进行测试，计算下载时间得到下载速度。因为 speed.cloudflare.com 容易返回 403，所以默认不再使用它作为测速入口。

当 server-url 不带 path 时 (使用 https://speed.cloudflare.com 或自建测速服务)，使用 /__down 和 /__up 完成下载与上传测试。
当 server-url 带 path 时，会被识别为直接下载地址，只进行下载测速。

如果你确认 https://speed.cloudflare.com 可以访问并希望测试上传，请显式设置为 full 模式，例如：
```shell
clash-speedtest --server-url "https://speed.cloudflare.com" --speed-mode full
```
或者你也可以自己搭建一个测速服务器，用来测试下载和上传速度：

```shell
# 在您需要进行测速的服务器上安装和启动测速服务器
> go install github.com/faceair/clash-speedtest/download-server@latest
> download-server

# 此时在本地使用 http://your-server-ip:8080 作为 server-url 即可
> clash-speedtest --server-url "http://your-server-ip:8080" --speed-mode full
```


测试结果：
1. 带宽 是指下载指定大小文件的速度，即一般理解中的下载速度。当这个数值越高时表明节点的出口带宽越大。
2. 延迟 是指 HTTP GET 请求拿到第一个字节的的响应时间，即一般理解中的 TTFB。当这个数值越低时表明你本地到达节点的延迟越低，可能意味着中转节点有 BGP 部署、出海线路是 IEPL、IPLC 等。

请注意带宽跟延迟是两个独立的指标，两者并不关联：
1. 可能带宽很高但是延迟也很高，这种情况下你下载速度很快但是打开网页的时候却很慢，可能是是中转节点没有 BGP 加速，但出海线路带宽很充足。
2. 可能带宽很低但是延迟也很低，这种情况下你打开网页的时候很快但是下载速度很慢，可能是中转节点有 BGP 加速，但出海线路的 IEPL、IPLC 带宽很小。

## License

[GPL-3.0](LICENSE)
