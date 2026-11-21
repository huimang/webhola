# 在线安装包验证

日期：2026-11-20，Windows x64。

## 已验证

- Inno Setup 6.7.4 编译 `native/build/Webhola-Setup-0.1.0-x64.exe` 成功，约 12 MB，包含当前 Webhola 程序、品牌图标与中文向导。提供当前用户安装、开始菜单入口、可选桌面图标及卸载程序。
- Wireshark 4.6.9 x64 与 Npcap 1.90 从官方 URL 下载成功，Authenticode 状态均为 Valid，发布者分别为 Wireshark Foundation 与 Nmap Software LLC。
- 下载步骤固定校验 SHA-256，启动依赖安装前再次验证官方发布者签名。无签名应用及把 Npcap 当作 Wireshark 的错误发布者测试均正确拒绝。
- Windows PowerShell 5.1 下的依赖检测、已配置 TShark 路径复用、配置写入及回读通过；额外配置属性保留，非法数组配置不会被覆盖。
- 依赖脚本测试仅使用项目 `.build/installer-test-*` 临时目录，不改动实际用户配置或驱动。
- 安装向导实际打开，已读取中文目标目录页和依赖任务页。后续调整为每次安装默认选择自动依赖任务；本轮不点击“安装”。
- 安装器源码、构建步骤和限制见 `installer/README.md`。

## JVM 依赖补充验证

- 官方 Temurin JDK 21.0.13.1+2 Windows x64 ZIP 和 Arthas 4.3.5 完整 ZIP 下载、固定 SHA-256 校验通过。
- Windows PowerShell 5.1 实际解压 JDK 和 Arthas 至项目测试目录，java -version 与 JAR 完整性验证、配置写入/回读通过。
- 错误哈希被拒绝；安装后无需下载即可复用；已有 TShark、其他属性及自定义 Arthas 端口保留，默认端口写入、非法数组配置保护通过。
- 使用此次解压的 JDK 和 Arthas 完整包，真实启动独立 JVM 并验证附加、HTTP memory 查询和断开通过。测试不连接现有业务进程。受限运行环境中的 Java 本地通信不可用，最终在允许本地通信的环境验证通过。
- Go 全部单元测试及在线安装器编译通过。新增 JVM 选项未进行全新 Windows 安装向导端到端验收。

## 验证范围

未执行当前系统的 Wireshark / Npcap 安装或升级，未完成全新 Windows 的 UAC、Npcap 官方许可窗口、驱动启用与重启完整验收。依赖被取消、未就绪或检测失败时，安装器会保留 Webhola 并在完成页提示；不能据此声称抓包已可用。

Npcap 免费版不提供静默安装，安装包不传递 `/S`，也不打包分发 Npcap 二进制。需要完全无人值守安装时需另行取得 Npcap OEM 授权，本版本未实现 OEM 部署。
