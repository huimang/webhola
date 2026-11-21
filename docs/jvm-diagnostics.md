# JVM 路由与诊断

本页记录连接配置、路由扫描、watch / trace、进程监控及诊断限制。

## 连接与使用

服务扫描会标记 `JVM` 和 PID：识别 `java.exe` / `javaw.exe`，也检查服务包装进程加载的 `jvm.dll`。JVM 端口即使未通过 HTTP 探测仍会列出，并标注“HTTP 未确认”；读取进程模块受权限和位数限制，无法识别时会明确提示。扫描不会自动 attach。

1. 在 **文件 → 设置…** 配置 JDK 的 `java.exe`、官方 `arthas-boot.jar` 和 Arthas HTTP 端口（默认 `8563`）。Java 留空依次查找 `WEBHOLA_JAVA`、`JAVA_HOME` 和 PATH；JAR 留空可使用 `WEBHOLA_ARTHAS_BOOT`。配置与 TShark 一同保存、回读，取消不保存。Arthas 为可选依赖，不影响抓包。
2. 扫描并选择已确认的 JVM 服务后，才显示 **JVM 路由与跟踪…** 入口，点击 **attach 并扫描路由**。未确认、非 JVM 服务或仅手动输入端口时不提供 Arthas 追踪。连接前重新核对端口/PID，连接后核对 Arthas 会话返回的 PID；默认端口被其他进程占用时会拒绝连接，需在设置中更换端口。已有且 PID 匹配的 Arthas 可直接复用。
3. 读取 Spring MVC / WebFlux `RequestMappingHandlerMapping` 的运行时注册表，顶部支持按 URL、HTTP 方法、条件、类或方法搜索，再从 **选择 URL 路由** 下拉框选择，自动填写对应类、方法及 ClassLoader。扫描涵盖该 JVM 的多个应用/管理端口，不保证每条映射都属于所选 HTTP 端口；映射不包含额外的 servlet context-path 或代理前缀。
4. **watch / trace** 的每次方法调用在左侧单独列出模式、方法、路由、时间、耗时和异常状态。选择记录后，右侧显示参数、返回结果、异常或调用树；JSON 默认格式化高亮，可切换原文、复制，并在独立全屏窗口查看（Esc 返回）。原始 Arthas 结果也保留为独立页签。观察点安装成功后再发起请求，也可使用原有的重新发送功能；不会自动重放历史请求。一轮观察持续到点击 **停止操作**、30 分钟或 100000 次命中为止。切换路由显示各自的记录，继续观察和方法下探会保留已有记录，清空不改变观察目标。
5. 从 trace 调用树点击 **下探**，自动查询该类的 ClassLoader，再次 watch / trace。同名类存在于多个 ClassLoader 时需要手动填写 hash；可直接编辑目标类和方法，禁止通配符和命令拼接。

JVM 跟踪在主窗体内以标签页显示，可与 HTTP 抓包切换，切换时保留连接、观察和记录。关闭 JVM 标签页会取消当前任务并关闭本次观察会话；标签页启动的 Arthas 执行 `stop`，复用的外部 Arthas 保持运行。`attach` 是连接，断开由会话清理/`stop` 完成。所有诊断端口仅绑定 `127.0.0.1`；HTTP 端口可配置，Bootstrap 4.3.5 的 Telnet 使用随机本地端口以避开负端口预检查问题。在线安装包准备完整 Arthas 组件，应用自动通过 `--arthas-home` 使用该目录；手动配置单独 Bootstrap 时仍可能下载组件。

当前支持 Spring 注解及程序注册的 HandlerMethod 路由，包含组合注解与继承映射；不支持其他框架、WebFlux 函数式路由、远程 JVM、容器 PID 映射和任意未加载类的静态扫描。每个框架/ClassLoader 最多读取 256 个 HandlerMapping 实例，达到上限或部分读取失败会提示。方法级观察包含同名重载及其他 URL 对该方法的调用；watch 的返回对象不是最终序列化 HTTP 正文，异步方法返回 Future/Publisher，trace 不跨异步线程自动串联。历史最多保留 100 条调用或约 16 MiB，每条参数、返回值、异常、原始结果分别最多预览 256 KiB；trace 节点最多 1000 个，单次 HTTP API 响应限制 8 MiB。达到上限时会标明截断；无法可靠解析旧版 watch 格式时仅显示原始结果，不推测字段。watch 使用 Arthas 自带的 JSON 序列化器，不修改其他会话共享的全局输出格式。

已用 JDK 21、Arthas 4.3.5、Spring MVC 5.3.24 验证 attach、动态路由、watch、trace、方法下探及停止；建议使用 Arthas 4.3.5 或更新版本。需使用兼容目标 JVM 且具备 attach 能力的 JDK，并拥有目标进程的相应权限。

连接前会使用所选 JDK 同目录的 `jcmd` 检查目标 JVM 元空间；剩余不足 16 MiB 时停止注入，并提示实际占用、上限及建议的 `MaxMetaspaceSize` 参数（需要修改目标应用并重启）。系统空闲内存不代表 JVM 元空间仍有容量；此检查是预防措施，不保证剩余足够时一定能启动。缺少 `jcmd`、读取权限或 JVM 不支持时仍尝试正常连接。注入后最多等待 5 秒让 HTTP 服务就绪，仍会核对目标 PID。

协议依据：[Arthas HTTP API](https://arthas.aliyun.com/en/doc/http-api.html)、[vmtool](https://arthas.aliyun.com/en/doc/vmtool.html)、[watch](https://arthas.aliyun.com/en/doc/watch.html) 与 [trace](https://arthas.aliyun.com/en/doc/trace.html)。

## 进程监控与参数名来源

JVM 跟踪页面显示固定目标的进程名称、PID、服务端口和可执行文件路径，每 5 秒检查存活状态并刷新工作集、私有提交、Java 堆及元空间。JVM 数据通过所选 JDK 的 jcmd 读取，不依赖 Arthas 已连接；权限不足或不支持的指标标为不可用。进程退出或 PID 被复用后隐藏内存数据，停止当前操作并禁用 attach/watch/trace，需关闭标签后重新扫描。

watch 参数名优先使用现有反射信息和 Spring 注解；在安装 watch 前调用 Arthas `jad` 反编译目标类并缓存签名，缺失参数名时按 ClassLoader 与完整参数类型匹配重载后补全。成功结果按连接、类、ClassLoader 和方法缓存；失败或无法唯一匹配时保留 argN，不影响观察。来自 jad 的名称单独提示来源，未保留调试信息时可能为反编译器生成，并非保证还原原始源码名称。
