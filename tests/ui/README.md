# WinForms UI 协议夹具

`FixtureTool.cs` 是无外网依赖的进程协议夹具。它不是测速算法测试，也不会启动代理或访问地区服务；它只模拟 GUI 实际依赖的两个相邻进程，供真实 WinForms 操作自动化使用。

## 一键准备

在项目根目录执行：

```powershell
$fixture = .\tests\ui\prepare-ui-fixture.ps1
$fixture | ConvertTo-Json -Compress
```

脚本会创建独立临时沙箱、编译并复制两个夹具 EXE、复制当前 GUI、生成输入和控制文件，并编译 `UiFixtureLauncher.exe`。返回值是可直接由自动化读取的对象，包含 `Sandbox`、`Launcher`、`Input` 和 `Output`。启动 `Launcher` 后，GUI 的设置目录、`TEMP`、`TMP` 和夹具根目录都会指向沙箱，不会读取或修改真实用户设置。

## 编译

在项目根目录执行：

```powershell
$compiler = 'C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (-not (Test-Path -LiteralPath $compiler)) {
    $compiler = 'C:\Windows\Microsoft.NET\Framework\v4.0.30319\csc.exe'
}

$fixtureExe = Join-Path $env:TEMP 'ClashSpeedTestGUI-FixtureTool.exe'
& $compiler `
    /nologo `
    /target:exe `
    /platform:anycpu `
    /optimize+ `
    /codepage:65001 `
    /utf8output `
    /out:$fixtureExe `
    /reference:System.dll `
    /reference:System.Core.dll `
    /reference:System.Web.Extensions.dll `
    .\tests\ui\FixtureTool.cs

if ($LASTEXITCODE -ne 0) { throw "Fixture build failed: $LASTEXITCODE" }
```

测试脚本应创建独立临时目录，并把同一个 EXE 复制成两个工具名：

```powershell
$sandbox = Join-Path $env:TEMP ('ClashSpeedTestGUI-UiTest-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $sandbox, "$sandbox\control", "$sandbox\signals", "$sandbox\work" | Out-Null
Copy-Item -LiteralPath $fixtureExe -Destination "$sandbox\subscription-parser.exe"
Copy-Item -LiteralPath $fixtureExe -Destination "$sandbox\speedtest-runner.exe"
$env:CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT = $sandbox
```

夹具根据当前 EXE 文件名自动选择 parser 或 runner 角色。直接调试原始 `FixtureTool.exe` 时，也可把 `--role parser` 或 `--role runner` 放在其他参数之前。

## 沙箱约束

环境变量 `CLASH_SPEEDTEST_GUI_UI_FIXTURE_ROOT` 是必填项，且目录必须已经存在。

- parser 输入、parser 输出、runner 配置、runner 输出、`-list-config` 输入和地区请求都必须位于该目录内。
- parser 拒绝读取沙箱外的输入；runner 也拒绝读取或写入沙箱外的路径。
- 夹具不会读取项目根目录的 `cs.yaml`、`wa.yaml` 或 `filtered.yaml`。
- 夹具没有任何 HTTP、DNS、代理或 Gist 调用。

UI 自动化启动 GUI 时，还应把 `TEMP` 和 `TMP` 指向沙箱内目录，并通过 GUI 后续提供的独立设置路径入口隔离真实用户设置。

## 固定测速数据

测速协议始终是严格 v3，支持 GUI 的 `fast`、`download` 和 `full` 三种表头。

| 稳定 ID | 名称 | 类型 | 延迟 | `usable` | 导出 |
|---|---|---|---:|---|---|
| 64 个 `a` | Fixture 香港 A | SS | 42ms | true | 是 |
| 64 个 `b` | Fixture 美国 B | Trojan | 420ms | true | 是 |
| 64 个 `c` | Fixture 失败 C | VLESS | 未测试 | false | 否 |

正常测速输出顺序为：

1. `@protocol\t3`
2. 当前模式的固定表头
3. `@nodes\t3`
4. 三个完整 `@nodejson`
5. 三个一一对应、带原始指标和 `usable` 的 `@resultjson`
6. 写出只包含 A、B 的两节点 YAML

`-list-config <path>` 要求文件已经存在，并返回只包含 A、B 的 JSON 节点清单。这与 GUI 对临时输出和最终输出各执行一次对账的流程一致。

## 测速控制文件

控制文件为 `control/speed-mode.txt`。不存在时默认 `success`。

| 值 | 行为 |
|---|---|
| `success` | 立即输出完整结果并写两节点 YAML |
| `gated-success` | 输出完整节点清单后等待 `control/speed-release.signal`，随后正常完成 |
| `block-after-manifest` | 输出完整节点清单后永久阻塞，只能由 GUI 取消并终止子进程 |

节点清单完成并 Flush 后会写 `signals/speed-started.json`；正常完成后写 `signals/speed-completed.json`。自动化应先删除旧 signal；使用 `gated-success` 时还必须删除旧的 `control/speed-release.signal`，再启动本轮任务，避免误读上一轮标志。

## 地区查询控制文件

控制文件为 `control/region-mode.txt`。不存在时默认 `all-success`。

| 值 | 行为 | 退出码 |
|---|---|---:|
| `all-success` | A=香港、B=美国，其他已知节点=日本 | 0 |
| `mixed` | A 成功；B 返回 `fixture region provider failure`；其他已知节点成功 | 0 |
| `block-after-one` | 输出并 Flush 第一个合法事件，然后永久阻塞 | 被 GUI 终止 |
| `malformed` | 第一个合法事件后输出非法 Base64 `@regionjson` | 0 |
| `missing` | 故意少输出最后一个事件 | 0 |
| `partial-nonzero` | 输出第一个合法事件后向 stderr 写原因 | 7 |

地区查询固定先输出 `@protocol\t2` 和精确的 `@regions` 数量。信号文件：

- `signals/region-started.json`：表头已 Flush。
- `signals/region-first-event.json`：第一个事件已 Flush，可安全测试停止、关闭窗口和事务回滚。
- `signals/region-completed.json`：非阻塞场景已走到预定结束点；`missing`、`malformed` 和 `partial-nonzero` 也会记录其故意失败的信息。

所有 signal 都通过同目录临时文件原子替换为 UTF-8 JSON，包含 `pid`、UTC 时间、角色、模式和说明。阻塞模式不使用固定时长 `Sleep` 判定就绪，测试应等待对应 signal 后再点击“停止”或关闭窗口。

## runner 调用覆盖

夹具识别 GUI 当前使用的三种 runner 入口：

```text
speedtest-runner.exe -c <prepared.yaml> ... -speed-mode <mode> -output <temporary.yaml>
speedtest-runner.exe -list-config <temporary-or-final.yaml>
speedtest-runner.exe -c <final.yaml> -region-query <request.json>
```

`-manage-config` 支持按稳定 ID 重命名和删除夹具导出的 A/B 节点，并会重写沙箱内 YAML、返回更新后的节点清单以及写入 `signals/manage-config-completed.json`。项目根目录的 `test-all.ps1` 会调用 `test-fixture-contract.ps1`，自动验证“测速导出 → 重命名 A → 删除 B → 重新读取”的完整相邻进程契约。

## FlaUI 操作级回归

根目录的默认 `test-all.ps1` 保持无桌面干扰。需要覆盖真实鼠标操作和弹窗视觉层时执行：

```powershell
.\test-all.ps1 -IncludeWinFormsUI
```

也可以只运行这部分：

```powershell
.\tests\ui\test-winforms-ui.ps1
```

测试通过稳定 Automation ID 驱动主窗口，并处理 WinForms 原生完成、保存和删除确认弹窗；实际完成“三节点测速 → 重命名 A → 删除 B → 校验 YAML”。结果表和右键菜单使用窗口内相对坐标定位，以避开 Windows 10 上 FlaUI 高层 `DataGridView` 行枚举可能阻塞的问题。

截图优先调用 `PrintWindow(PW_RENDERFULLCONTENT)`，失败或得到空白图像时回退到屏幕区域 `BitBlt`，不依赖会报 `SetIsBorderRequired 0x80004002` 的 Computer Use 捕获链。首次执行会下载并校验固定版本 FlaUI 包，缓存在 `%LOCALAPPDATA%\ClashSpeedTestGUI\test-tools\FlaUI\5.0.0\`；不会安装系统组件、注册快捷键或操作 PixPin。成功截图和日志位于 `tests\ui\artifacts\`，失败时同目录保留追踪和截图用于排查。
