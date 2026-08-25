# tunnet-lite

[English](README.md)

TunNet 数据面的开源后端：一个把 `xray-core` 当库用的本地 SOCKS 代理，说的是和原版客户端同一套隧道协议，并带一个用于切换线路的交互式终端控制台。

```
SOCKS 127.0.0.1:18080
  -> VLESS（VLESS Encryption native/1-RTT，XTLS Vision）
  -> XHTTP stream-up（GET 下行 / POST 上行）
  -> HTTP/2 over TLS 1.3 + ECH（uTLS Chrome 133）
  -> 运营商前置代理（HTTP CONNECT）[可选]
  -> CDN 边缘 -> 出口节点
```

**没有任何东西硬编码在程序里**。账号身份、每个出口独立的加密公钥、轮换的根域名、运营商入口池，全部在运行时来自控制面，或来自你自己提供的节点清单文件。

没有图形界面、不起 HTTP 服务、不依赖任何 UI 框架：控制台从 stdin 读命令，所以在 SSH 里和在本地用起来完全一样。

## 安装

```bash
npm install -g tunnet-lite
tunnet-lite -refresh
tunnet-lite -console
```

程序本身是 Go 二进制而不是 JavaScript；用 npm 只是为了把它装到机器上并让 `tunnet-lite` 进 PATH。二进制放在按平台拆分的包里，由 npm 的 `os` / `cpu` 字段选中，所以安装只会下载**一个约 12 MB** 的包，而不是六个平台的构建。

这些平台包被声明为 `optionalDependencies`，正是为了让不匹配当前主机的那五个被跳过而不是导致安装失败——反过来说，用 `--omit=optional` 安装会什么都装不到，这种情况下启动器会明确告诉你。

如果你不想经过 npm，每个 GitHub Release 也附带了预编译二进制。

## 从源码构建

`xray-core` 通过 `replace` 钉死到同级目录的一份 checkout，并且需要 `patches/` 里的两个补丁——运营商前置代理和入口故障转移都依赖它们。这一步只做一次：

```bash
git clone https://github.com/XTLS/Xray-core.git ../xray-tunnet
git -C ../xray-tunnet checkout f02a35786124a6ad046727f2408e32317cc19a41
git -C ../xray-tunnet apply "$PWD/patches/xray-core-http-outbound.patch"
```

然后：

```bash
go build -o tunnet-lite ./

# 一次性：创建身份并拉取节点目录。
# 首次运行会打印验证链接，浏览器批准后再运行一次。
./tunnet-lite -refresh

./tunnet-lite -console
```

```
> exits
   1  tyo-01         日本 TYO 01     online
   7  sin-01         新加坡 SIN 01    online
> exit sin-01
exit set to sin-01 — 新加坡 SIN 01. Run "start" to apply.
> start
state        running since 22:49:33
listening    socks5://127.0.0.1:18080 (tcp only)
exit         sin-01 — 新加坡 SIN 01
```

不加 `-console` 就直接起代理并一直服务到被中断，适合放在服务管理器下：

```bash
./tunnet-lite -host tyo-01 -entry-group 电信
```

`nodes.json`、`tunnet-lite-identity.json`、`tunnet-lite-pins.json` 分别是你的账号凭据、签名私钥和已固定的证书。三个都在 `.gitignore` 里，**绝不能提交到版本控制**。

### 让流量走它

```bash
curl --proxy socks5h://127.0.0.1:18080 https://api.ipify.org
```

**用 `socks5h`，不要用 `socks5`。** 结尾那个 `h` 表示域名在代理侧解析。裸 `socks5` 会在本地走 UDP 解析，而这些节点不承载 UDP（见下文），等于把查询漏在了隧道外面。Firefox 里对应的开关是 `network.proxy.socks_remote_dns`。

### 控制台命令

| 命令 | 作用 |
|---|---|
| `status` | 显示当前运行状态 |
| `exits` | 列出出口节点 |
| `entries` | 列出运营商入口 |
| `exit <slug\|编号>` | 选择出口节点 |
| `entry <名称\|编号>` | 选择运营商入口 |
| `udp on\|off` | 开关 SOCKS UDP associate |
| `direct on\|off` | 绕过前置代理 |
| `route global\|smart` | 选择隧道承载多少流量 |
| `start` | 应用当前选择 |
| `stop` | 停止代理 |
| `refresh` | 拉取最新节点目录 |
| `quit` | 退出，代理随之停止 |

**选择在执行 `start` 之前不会生效**——`start` 会重新探测入口池并重建隧道。

### 常用参数

| 参数 | 含义 |
|---|---|
| `-console` | 进入交互控制台，而不是只做服务 |
| `-refresh` | 从控制面拉取节点目录后退出 |
| `-host tyo-01` | 指定出口 slug（默认：第一个在线出口） |
| `-entry-group 电信` | 按名称或子串指定运营商入口 |
| `-no-front` | 直连 CDN，跳过前置代理 |
| `-root <域名>` | 钉住某个根域名，不用缓存的选择 |
| `-dump-config` | 打印生成的 Xray 配置后退出 |
| `-port 18080` | SOCKS 端口 |
| `-route smart` | 中国大陆目标直连（需要规则集） |
| `-assets <目录>` | `geoip.dat` 和 `geosite.dat` 所在目录 |
| `-refresh-interval 6h` | 后台监视节点变化并提示（永不自动应用） |
| `-doh <地址>` | 逗号分隔的纯 IP DoH 上游；`off` 表示用系统 DNS |
| `-ech=true` | 控制面连接强制要求 ECH，失败即中止 |
| `-pin-mode tofu` | 证书固定模式：`off`、`tofu`、`strict` |
| `-pins <路径>` | pin 存储路径（默认 `tunnet-lite-pins.json`） |

## 分流

两种模式。两者都会把私有、环回、链路本地、运营商 NAT、组播和保留地址段直接送出去——这些走代理没有意义。这份列表是**直接写在配置里**的，而不是引用 `geoip:private`，所以默认模式完全不需要数据文件。

| 模式 | 行为 |
|---|---|
| `global`（默认） | 其余全部走隧道 |
| `smart` | 中国大陆目标也留在本地 |

smart 模式需要两个规则集。它们**不内嵌**，这样你能看清当前生效的是哪些规则，也能自行更新：

```bash
sh tools/fetch-rules.sh assets
./tunnet-lite -route smart -assets assets
```

脚本会先用官方发布的摘要校验，通过了才安装。这不是形式主义：**下载被截断时文件看起来是好的**，问题会在很久之后以 geo 数据加载器一句没头没脑的 `EOF` 暴露出来。

实测，同样两个目标在两种模式下：

| 目标 | `global` | `smart` |
|---|---|---|
| `www.baidu.com` | 隧道 | **直连** |
| `api.ipify.org` | 隧道 | 隧道 |

规则按顺序求值，且**域名规则排在 IP 规则之前**。这个顺序很关键：`socks5h` 客户端交过来的是**域名**，而在默认的 `AsIs` 策略下域名永远不会匹配 IP 规则。所以 `geosite:cn` 负责按名字的目标，`geoip:cn` 覆盖直接连 IP 的情况。加 `-route-domain-strategy IPIfNonMatch` 可以让路由器把未匹配的域名在本地解析后再用 IP 规则判断——更准确，代价是每个未匹配域名都要做一次本地解析。

原版客户端做的是同样的分流，用的是同一套规则集的自有副本，gzip 压缩后内嵌在二进制里，名字叫 `builtin:smart`。

## 控制面

`-refresh` 执行签名且加密的控制面交换：

1. **bootstrap** —— 声明身份。全新身份会返回一个票据和一个验证 URL。
2. **批准** —— 由人打开那个 URL。该 URL 位于会轮换的根域名上，所以是从响应里解析出来的，不是写死的。`-auto-approve` 会直接调用验证页面调用的同一个接口，这只在操作你自己的账号时才说得通。
3. **access** —— 用已批准的票据换取完整目录。
4. **sync** —— 后续运行时刷新已批准的身份。

低于版本门槛的客户端拿不到目录：响应里只有一个 release 块、没有 runtime 段，表现为一句莫名其妙的
「no runtime section」而不是「请升级」。门槛值就写在同一个响应里，所以客户端会采用它并重试一次。
这样在厂商抬高门槛之后，节点变更仍然能继续同步进来，而不是悄无声息地过期。

请求用 RFC 9421 HTTP Message Signatures 签名，覆盖 method、authority、path、content-type、digest 和两个密钥头。响应用 HPKE 密封到每次请求新生成的 X25519 密钥上，并绑定操作名、client id 和请求 nonce，所以即便中途终止 TLS 也读不到目录。

在这次交换之前，客户端会通过原版客户端使用的同样两个纯 IP DoH 服务，解析控制面端点的 A、AAAA 和 HTTPS 记录。ECH 配置来自 HTTPS 记录。**ECH 默认是强制的**：拿不到配置、或者握手中服务端不接受它，都会中止连接，而不是悄悄暴露主机名。要放弃这层保护得显式写 `-ech=false`；用 `-doh=off` 同样需要这个显式选择。

常规 Web PKI 校验之后还会做 SPKI 固定。默认的 `tofu` 模式在首次连接时记录已验证的证书链，之后拒绝任何与这些公钥毫无交集的链。`strict` 则在没有已存 pin 时拒绝首次连接。pin 文件是一个本地信任锚，不纳入版本控制。

两种响应的信封不同，这一点很重要：

| | access | sync |
|---|---|---|
| 信封 | `bootstrap.runtime` | `runtime`（顶层） |
| hosts、入口池 | 有 | 有 |
| `network.root_domains` | 有 | **无** |

所以 sync 是**增量更新**，会合并到已知内容之上。这也是根域名要在本地缓存的原因：根域名池只随完整的 access 响应下发一次。

### 跟上节点变化

出口会上下线，每个出口的密钥会轮换，入口 IP 池会重排。**默认不做任何轮询。** `-refresh` 拉取一次后退出；控制台里的 `refresh` 更新内存中的节点集，`start` 才应用。

`-refresh-interval` 会在后台监视并报告变化：

```
nodes changed — exits removed: google-gemini; address pools changed: 电信接入点
the running tunnel still uses the previous set; apply it when convenient
```

**它到此为止。** 应用新节点集意味着重建隧道、掐断所有进行中的连接；什么时候可以承受这个代价，该由使用代理的人决定，而不是由一个定时器决定。

## 入口选择怎么工作

刻意用了两套机制：

1. **启动时：TCP RTT 排序。** 并发拨测所选入口池里的每个地址，不可达的直接剔除，其余按 RTT 排序，最快的成为负载均衡器的 `fallbackTag`。开销很小，和原版客户端的做法一致（`measureTCPRTT`）。
2. **运行期：Xray 健康检查。** 存活的地址全部作为池成员挂在 `leastPing` 均衡器下，由 `burstObservatory` 驱动，所以入口劣化时无需重启即可切换。

有两个健康检查参数是**关键约束**，已在代码里强制：

- **`interval` 必须大于 `timeout`。** 轮次重叠会互相取消，然后**每一个**入口——包括健康的——都会报 `io: read/write on closed pipe`。
- **`timeout` 必须给足。** Xray 默认的 5 秒在这条隧道上完不成一次探测，因为 `burstObservatory` 关闭了 keep-alive，每次探测都要建立一条完整的 VLESS Encryption + XHTTP + TLS/ECH 会话。这里默认 15 秒。

## UDP 无法穿过这些节点

`-udp` 会接通 SOCKS UDP associate，监听器确实能在本地接收并路由 UDP —— 但没有任何回包。**这是运营商节点的性质，不是这里的实现缺失。**

链条是被强制的：XTLS Vision 在本服务上是必需的（见下文），而 Vision 启用时 Xray 会把**所有** UDP 关联都走 mux。这些节点根本不应答 mux —— 给普通 TCP 请求开启 mux 会以完全相同的方式失败，这正是隔离出该结论的对照实验。

请用 `socks5h://`，让域名在代理侧走 TCP 解析。原版也是这么绕开的：它的客户端内置了两个 DoH 上游，而不是指望隧道里的 UDP。

这个开关予以保留，并在每次启动时告警，方便运营商配置变化后重新验证。

## 线路参数

这些参数是**对着真实节点扫描出来的，不是读代码读出来的**。

| 参数 | 取值 | 证据 |
|---|---|---|
| xorMode | `native`（0） | `xorpub` 和 `random` 都被拒绝：服务端在收到 1333 字节 client hello 后立刻关闭两条 HTTP/2 流 |
| 握手 | `1rtt` | `0rtt` 在首次连接时行为完全一致 |
| padding | `100-35-35` | 产生单个 35 字节填充，即 1333 字节的 hello |
| flow | `xtls-rprx-vision` | **必需**：有它 4/4，没有 0/4 |
| XHTTP 模式 | `stream-up` | GET 下行，POST 上行且带 `Content-Type: application/grpc` |
| TLS | uTLS Chrome 133、ALPN `h2`、ECH | ECH public name 来自该主机的 HTTPS 记录 |
| mux | 节点不支持 | 开启 mux 的 TCP 会失败；这正是 UDP 走不通的原因 |

`native` 是其中最关键的一个。早先的分析从反汇编推断成了 `xorpub`，结果每条连接都被重置；是对这三个取值做扫描才找出了错误。

`flow` 有一段值得保留的教训。它一度被记录为「可选」，因为一次扫描显示有它和没它结果相同。**那次扫描是错的**：空的 `Flow` 被当作「使用默认值」，所以那些看似没有 flow 的行其实也在跑 Vision。禁用它需要显式的 `-flow none`，有了这个之后答案就毫不含糊了。

这个教训可以推广——**当扫描告诉你某个参数无关紧要时，先确认工具真的能表达那个否定分支。**

## 被钉死的依赖，以及如何升级

`go.mod` 通过 `replace` 把 `xray-core` 钉死到某个具体提交：

```
require github.com/xtls/xray-core v0.0.0-20260824000000-f02a35786124
replace github.com/xtls/xray-core => ../xray-tunnet
```

这份 fork 是上游提交 `f02a35786124a6ad046727f2408e32317cc19a41` 加上下面描述的两个修复。你自己发布时，把 `replace` 指向你发布的 fork。

**绝不要随手升级 `xray-core`。** VLESS Encryption 是个很新、迭代很快的特性。运营商的服务端是某个上游版本的冻结 fork；今天的上游恰好和它在 1333 字节握手上达成一致，而这种一致是**此刻的性质，不是保证**。

把升级当成一次必须重新验证的变更：

```bash
go build -o tunnet-lite ./
python3 sweep/sweep.py --nodes nodes.json --attempts 2
```

只有当某一行通过时，才把它的参数作为新默认值写进 `internal/xcfg/xcfg.go`，并把证据记录在文档里。**如果一行都不通过，说明线路格式变了：回滚 pin，而不是去猜参数。**

## 两个 xray-core 补丁

两个都在 `proxy/http/client.go` 里，都是与本项目无关的通用缺陷，都值得提交到上游，这样这份 fork 最终可以消失。

**1. `headers` 无法覆盖 CONNECT 的 `Host`。** Go 的 `Request.Write` 会排除 `Header["Host"]` 而改用 `req.Host`，所以配置里的 `Host` 根本到不了线上。但只把它转写到 `req.Host` 也不够：对 CONNECT，Go 还会**从 `req.Host` 推导请求行的 authority**，于是隧道目标被一起改写——症状是 TLS 握手落到了前置代理自己的证书上。修法是先把真实目标钉进 `req.URL.Opaque`。

没有这个补丁，靠 `Host` 覆盖做认证的运营商前置代理**完全无法使用**。

**2. 配了 `headers` 的 outbound 无法被健康检查。** `fillRequestHeader` 只要配置了任何 header 就硬性要求 inbound 会话元数据，而 `burstObservatory` 是通过 `tagged.Dialer` 分发的，不携带 inbound。对这类 outbound 的每次探测都会立刻以 `io: read/write on closed pipe` 失败，于是 `leastPing` 永远拿不到数据，均衡器永久退回 fallback。修法是只在 header 值真的含 `{{` 模板时才要求元数据。

没有这个补丁，只要配置了前置代理，**入口故障转移就是静默失效的**。

## 目录结构

| 路径 | 职责 |
|---|---|
| `internal/control` | 签名并 HPKE 密封的控制面调用、身份持久化 |
| `internal/inventory` | 节点清单加载、校验、选择、增量合并 |
| `internal/probe` | 入口池的并发 TCP RTT 排序 |
| `internal/xcfg` | 渲染 Xray 配置；保存已验证的线路参数 |
| `internal/engine` | 持有运行中的 xray-core 实例，可重启 |
| `internal/supervisor` | 解析选择、排序入口、应用方案 |
| `internal/console` | 交互式终端前端，零依赖 |
| `internal/resolver` | 纯 IP 上游的 DoH 解析与 ECH 配置查询 |
| `internal/pinning` | 证书链 SPKI 固定 |
| `tools/access2nodes` | 把已解密的响应转成节点清单 |
| `sweep/` | 线路参数重新验证 |

## 未实现

- **TUN。** 这里没有接，但零件是齐的：钉住的 `xray-core` 自带基于 gVisor 的 `tun`
  入站，Windows 上用 wintun——和原版客户端是同一套组件。缺的是它周围的工作。TUN
  网卡需要管理员权限；需要自己一套 DNS 配置（否则系统查询会进隧道，而 UDP 在这些节点上走不通，
  且控制面域名和节点域名的查询会成环）；装默认路由是**全机生效**的改动，清理不干净会让整台机器断网。
  在这些做完之前，请让流量走 SOCKS 监听。
- **端到端 UDP。** 见上文；被节点挡住，不是本代码的问题。

## 信任模型

**在假设这个客户端能保护你之前，请先读这一节。**

**运营商仍然能看到你的流量。** 出口节点终结你到互联网的连接。重写客户端不改变这一点：隧道之上没有端到端加密的一切，对运行出口的人都是可见的，和用原版客户端完全一样。

**只有客户端是开源的。** 控制面、CDN 入口层和出口节点都是运营商的闭源基础设施。你可以审计这个程序发送什么、信任什么；你无法审计隧道结束之后发生了什么。

**是什么在认证节点目录：加固过的 TLS。** 控制面响应用 HPKE 密封到每次请求新生成的 X25519 密钥上，这提供**机密性**——路径上的观察者读不到目录。但它不提供**真实性**：HPKE base 模式没有发送方认证，任何能读到请求（其中带着响应公钥）的人都能密封一份伪造的目录回来。所以真实性来自常规证书校验，**加上**本地存储的控制面端点 SPKI 固定。

在一次干净的 TOFU 初始记录之后，机器上后来出现的流氓根证书——企业 TLS 审计或恶意软件——无法替换目录，除非它的证书链也能匹配已存的某个公钥。**首次 TOFU 连接仍然是决定性的**：如果攻击者在初始记录时就已经在场，它可以让你记下错误的 pin。当这个风险重要时，请使用事先审核过的 pin 文件配合 `-pin-mode strict`。

**数据面是强绑定的，但只在这之后。** VLESS Encryption 用目录里的 X25519 公钥认证每个节点，所以攻击者即使拦截了 TLS 层，没有该节点的私钥也完不成握手。**这个保证的可靠程度，取决于它的起点——那份目录。**

### 控制面的残余风险

客户端现在与原版在三项相关的传输控制上对齐：专用 DoH、ECH、证书固定。TOFU 是一种运维上的折中，而非厂商签名的 pin 分发渠道。删除或替换本地 pin 文件会重置这个信任决定；程序**永远不会**自动更新不匹配的 pin。

## 许可证

本仓库的代码采用 MIT（见 `LICENSE`）。

它链接了 `xray-core`，后者采用 **Mozilla Public License 2.0**。`patches/` 里的补丁修改的是 MPL 覆盖的文件，因而仍受 MPL 约束；把它们以补丁形式发布，正是为了让这些修改保持可见、可归属。从本仓库构建出的二进制是组合作品：**分发时请同时遵守两个许可证。**

## 使用

这是一个互操作客户端，它期待的是**你自己的**凭据。不要发布节点清单或身份文件：`client_id` 是账号级凭据，把它连同节点目录一起分发，等于在再分发一个付费服务的访问权，而不是在分享一份实现。
