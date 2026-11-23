# 开发指南

## 源码构建

以下命令从仓库根目录执行。桌面构建使用 Go 1.27.4，脚本会选择对应工具链。当前主要验证平台为 Windows x64。

```powershell
./native/build.ps1
./native/build.ps1 -Run
./native/build.ps1 -Demo
```

产物为 `native/build/Webhola.exe`。Windows 运行抓包功能需安装 Npcap；Linux 需要 root 或 `CAP_NET_RAW`，macOS 需要 BPF 访问权限。JVM 诊断按需准备兼容目标 JVM、支持 attach 的 JDK 和 Arthas。

图标嵌入资源在 `native/internal/desktop/resources/`，构建脚本从品牌素材同步。Windows 文件版本定义在 `native/resources/webhola.rc`，由已跟踪的 `native/resource_windows_amd64.syso` 提供链接输入；更新版本或品牌资源后运行：

```powershell
./native/build-resources.ps1
```

资源重建需要 Windows SDK 和 Visual Studio Build Tools；普通构建使用现成资源文件。安装包构建见[安装器说明](../installer/README.md)。

## 测试

```powershell
cd native
go test ./...
go vet ./...
```

`internal/capture` 的默认测试使用手工构造的 IP 包和生成的 PCAP/PCAPNG 文件，不要求安装抓包驱动。设置 `WEBHOLA_INTEGRATION=1` 后，桌面包还会尝试在本机 loopback 上实时抓取选定端口的请求，需要 Npcap 与相应权限：

```powershell
$env:WEBHOLA_INTEGRATION='1'
go test ./...
```

真实 JVM 集成测试只启动并连接独立测试服务。准备 `WEBHOLA_JAVA`、`WEBHOLA_ARTHAS_BOOT`、`WEBHOLA_SPRING_CLASSPATH`（使用系统路径分隔符拼接 Spring MVC 与 servlet JAR）后，在 `native/` 中运行：

```powershell
$env:WEBHOLA_ARTHAS_INTEGRATION='1'
go test ./internal/desktop -run TestArthasRealJVMIntegration -v -timeout 120s
```

Npcap 命令行工具单独构建和测试，见[工具说明](../tools/npcap-probe/README.md)。未实机验证的平台应明确标记；交叉编译成功不代表抓包和界面行为已验收。

## 代码与提交约定

源码按[架构说明](architecture.md)中的功能包组织。完整协作规则保存在根目录 [AGENTS.md](../AGENTS.md)。

- 一个 commit 表达一个完整、独立的功能、修复或重构，包含相关实现、测试和必要文档；不把全部功能压成一次提交，也不按单个文件机械拆分。
- 依赖项先提交，每个功能提交尽量可独立构建和检查。
- commit 标题和正文均使用英文，采用 `type(scope): imperative summary`。
- `drafts/`、下载缓存、运行记录与构建产物不纳入 Git；已跟踪的构建输入资源保留。
- 版本发布单独同步应用、安装器和当前文档的版本，标签指向最终发布提交；历史验证文档保留当时版本。
- 改写历史前备份；推送和强制推送需要用户授权。
