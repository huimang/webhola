# Webhola Windows 在线安装包

产物：`native/build/Webhola-Setup-0.2.0-x64.exe`。默认将 Webhola 安装到当前用户的 `%LOCALAPPDATA%/Programs/Webhola`，创建开始菜单入口，并提供正常的 Windows 卸载程序；桌面快捷方式可选。

## 用户安装流程

1. 运行安装包，保持“自动安装 HTTP 抓包组件”选中。
2. 安装包检测已有 Npcap，仅在缺少时下载。
3. 下载文件先校验固定 SHA-256，再校验 Windows Authenticode 签名及发布者；启动前再次检查签名。下载或校验失败可重试，不执行失败文件。
4. 缺少 Npcap 时自动打开官方下载的 Npcap 安装程序，由用户确认许可、管理员授权及安装选项。免费版不使用 `/S`；完全静默部署需要 Npcap OEM 授权与相应安装器，本版本不提供该流程。
5. 安装后检查 Npcap 的 DLL 与系统服务，在完成页提示结果；失败、取消、权限不足或需要重启时明确说明，不宣称抓包已就绪。

不需要联网或暂时不抓包时，可取消依赖任务，仅安装 Webhola。已安装的 Npcap 不随 Webhola 卸载；用户配置也保留。

## JVM / Arthas 依赖

安装向导默认提供独立的“自动准备 JVM 诊断组件”选项，可单独选择，不依赖抓包任务。

- 检测已配置路径、环境变量、PATH 与本应用管理目录，优先复用能运行的 64 位完整 JDK（包含 javac、jcmd）与 Arthas 完整包。
- 缺少时下载 Temurin JDK 21.0.13.1+2（x64 ZIP，约 196 MiB）及 Arthas 4.3.5 完整包；固定校验 SHA-256 后，在安装目录的 `webhola-dependencies` 下解压并验证。无需修改系统 PATH / JAVA_HOME，也不覆盖共享 JDK。
- 仅有 arthas-boot.jar 时会准备完整发行包。应用检测到完整包后显式指定 `--arthas-home`，避免诊断首次启动再下载组件。
- 自动保存 `javaPath`、`arthasBootPath`；默认 HTTP 端口为 8563，保留已有端口及其他配置。非法配置不会被覆盖。
- 依赖就绪不代表目标 JVM 已连接；目标版本兼容性、同用户权限与 attach 限制仍需满足，可在设置中切换 JDK。安装器不自动附加业务进程。

共享依赖和配置不会被卸载程序删除；解压的 `webhola-dependencies` 目录也保留，确认不再使用时可手动删除。

执行 `./installer/Test-JavaDependencies.ps1` 可在项目临时目录验证缺失检测、真实压缩包安装、哈希拒绝、路径复用、配置保留和非法配置保护。该测试需预先将两个官方压缩包下载到 `.build/installer-tools`，或通过参数提供路径。

来源：[Adoptium 官方 API](https://api.adoptium.net/v3/assets/latest/21/hotspot?architecture=x64&image_type=jdk&os=windows&vendor=eclipse)、[Arthas 完整发行包](https://arthas.aliyun.com/en/doc/download.html)。

## 固定验证版本

|组件|版本|来源|发布者|
|---|---|---|---|
|Npcap|1.90|npcap.com/dist|Nmap Software LLC；也接受 Insecure.Com LLC 签名|

版本、URL 与固定 SHA-256 位于 `Webhola.iss`。更新依赖版本时必须从官方网站获取并校验签名，再更新哈希。运行安装包时按这些已验证版本获取依赖，不动态选择未经本项目验证的新版本。

## 构建

使用 Inno Setup 6.7+。项目缓存已有编译器时自动使用 `.build/installer-tools/inno/ISCC.exe`；也可显式传入：

```powershell
./installer/build.ps1 -Compiler 'C:\Program Files (x86)\Inno Setup 6\ISCC.exe'
```

已构建应用时可追加 `-SkipAppBuild`。简体中文语言文件从 Inno Setup 官方源码获取并缓存在 `.build/installer-tools/ChineseSimplified.isl`。构建工具、下载缓存和依赖安装文件不打入 Webhola 安装包。

## 验证

```powershell
./installer/Test-Dependencies.ps1 -NpcapPackage 'D:\downloads\npcap-1.90.exe'
```

测试只在项目 `.build/installer-test-*` 目录写状态文件，验证 Npcap 检测、官方签名接受以及无签名文件或错误发布者拒绝，不实际安装驱动。安装向导编译通过不等于已完成全新 Windows 上的驱动安装、UAC 与重启实机验收。

官方依据：[Npcap 安装说明](https://npcap.com/guide/npcap-users-guide.html)。
