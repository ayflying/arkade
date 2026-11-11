# GoFrame DAO 代码生成器

## 为什么需要这个目录

`gf gen dao` **直接在本仓库根目录（`hub/`）执行会失败**，报：

```
exception recovered: runtime error: index out of range [1] with length 0
```

**当前 CLI 输出未定位到真实越界点**：报错里出现的
`go.opentelemetry.io/otel/sdk/trace.(*recordingSpan).End` 只是 panic 传播
路径上的一帧，**不能据此断定真实错误被它吞掉**——otel 的 `End` 通常只是
repanic 原 panic，看到的栈也可能是工具输出格式截断造成的。

本目录不推测 panic 的来源，只如实记录"哪些条件下失败、哪些条件下正常"。

本目录提供一个**隔离的生成环境**，绕开这个问题，让生成可以随时重跑。

## 复现条件（实测观察）

**首先必须说清楚**：下面的表格是**观察到的相关现象**，不是已证实的根因。

**没有证据表明根因是依赖解析。** 也可能是 CLI 内部某个与 `go.mod` 内容无关的
路径处理或初始化顺序问题，恰好被依赖数量和模块图大小影响了执行时序。
本目录**不做根因断言**，只记录"哪些条件下会失败、哪些条件下不失败"。

| 条件 | 结果 |
|---|---|
| 在 `hub/` 下执行，`hub/go.mod` 的五个直接依赖（BurntSushi/toml、gofrs/flock、golang-migrate、gorilla/websocket、golang.org/x/crypto）**全部存在** | ❌ panic |
| 去掉其中任意一个（五个全测过，逐一去掉） | ✅ 正常 |
| 只保留 gf + gf sqlite driver | ✅ 正常 |
| 任意两个依赖组合 | ✅ 正常 |
| 换 gf CLI v2.10.0（全局版本） | ❌ 同样 panic |

隔离 workaround 有效，**不代表底层 bug 已定位或已修复**。
如果将来 gf CLI 修复了，本目录仍可继续用（它只是提供一个干净的生成环境）。

### 关于报错信息

```
exception recovered: runtime error: index out of range [1] with length 0
```

**当前 CLI 输出未定位到真实越界点。** 报错里出现
`go.opentelemetry.io/otel/sdk/trace.(*recordingSpan).End` 这一帧，只是
panic 传播路径上的一层，**不能据此认定真实错误被它吞掉**
（otel 的 `End` 一般只是 repanic；看到的栈也可能被工具的输出格式截断）。

`GF_DEBUG`、`--gf.gtrace=false`、`OTEL_SDK_DISABLED` 都试过，均未能让
越界发生的位置显现出来。在拿到可信的定位之前，本目录不对 panic 成因下任何结论。

## 使用方法

```bash
# Windows
pwsh ./tools/gfgen/gen.ps1

# Linux / macOS / WSL
bash ./tools/gfgen/gen.sh
```

脚本会：

1. 建一个临时模块（`module arkade/hub`，**与 hub 同名的 module**，
   这样生成物里的 `arkade/hub/internal/dao/internal` import 原样正确）；
2. 只 require gf v2.9.6 + gf sqlite driver v2.9.6（避开上述 panic 条件）；
3. 建一个空库，用 **hub 自己的迁移代码**迁移到最新版（不在临时目录里复制 SQL）；
4. 调 gf CLI 生成到临时模块的 `internal/`；
5. 把 `dao/`、`model/do/`、`model/entity/` 覆盖到 `hub/internal/`；
6. 清理临时目录。

## 为什么用 hub 自己的迁移代码建库

表结构必须来自**权威来源**。手工维护建表 SQL 会与真实迁移脱节，
生成出的 DAO 就会与线上结构不一致。

脚本通过 `go run` 调用 `hub/internal/dbmigrate`，保证临时库的结构
与生产库经过完全相同的迁移路径。改迁移 → 重跑本脚本 → DAO 自动跟上。

## 不会覆盖什么

`hub/internal/dao/*.go` 里 `// Add your custom methods and functionality below.`
之后的内容属于手写扩展区。本脚本覆盖的是**整个文件**——
所以自定义方法应放在**单独的文件**里（例如 `hub/internal/dao/custom_xxx.go`），
不要直接写进生成文件，否则重跑会被覆盖。

`internal/model/do`、`internal/model/entity` 同理，不要手改。

## gf CLI 版本

脚本使用独立安装的 gf v2.9.6：

```
D:\gopath\gf296\gf.exe        # Windows
$HOME/gopath/gf296/gf        # Linux（可用 GF_BIN 覆盖）
```

必须与 `hub/go.mod` 里的 gf 版本一致，否则生成物可能与运行时 API 不匹配。

用 `GF_BIN` 环境变量可覆盖路径。
