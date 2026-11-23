# OmniShare 公开交付服务

公开交付服务是可独立部署的 Go 程序。收件人在浏览器打开领取链接即可查看文档或下载指定附件。OmniShare 通过同一 HTTPS 地址上的受认证接口发布、查看和撤销交付。未配置交付服务器的设备会禁用公开交付。

## 部署

1. 使用 Go 1.26 构建服务。在 PowerShell 中从项目根目录执行：

   ```powershell
   go -C delivery-server test ./...
   $env:GOOS = 'linux'
   $env:GOARCH = 'amd64'
   $env:CGO_ENABLED = '0'
   go -C delivery-server build -trimpath -o ../omnishare-delivery ./cmd/omnishare-delivery
   Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
   ```

2. 在 Linux 上将二进制安装至 /opt/omnishare-delivery/omnishare-delivery。将 deploy/omnishare-delivery.service 安装到 /etc/systemd/system/，在 /etc/omnishare-delivery/server.env 设置 PUBLIC_ORIGIN，值是收件人打开的 HTTPS 源地址。
3. 给每个发布设备生成独立的 32 字节随机密钥，使用小写十六进制保存这 64 个字符。系统管理员通过安全通道将密钥交给发布端。服务器的 /etc/omnishare-delivery/publishers.json 仅保存设备名与这 64 个 ASCII 字符的 SHA-256 小写十六进制摘要。将配置文件权限设为 0600。
4. 运行 systemctl daemon-reload 和 systemctl enable --now omnishare-delivery。配置具有受信证书的 HTTPS 反向代理，将 /delivery/ 转发到 127.0.0.1:18484，将下表列出的管理调用转发到 127.0.0.1:18485。两组路由使用同一个受信 HTTPS 地址。参考 [Nginx 示例](deploy/nginx.conf.example)，将限流区配置放在 http 段，将路由放在 HTTPS server 段。验证 GET /delivery/health 返回 204，未带发布凭据的 GET /v1/deliveries 返回 401。

## 在 OmniShare 中配置

在桌面应用打开「公开交付 → 配置交付服务器」，使用管理员提供的三项信息。

| 字段 | 应填内容 |
| --- | --- |
| 服务器 HTTPS 地址 | 与服务端的 `PUBLIC_ORIGIN` 一致，例如 `https://delivery.example.com`。这个地址同时用于 OmniShare 发布和收件人领取。填写根地址，可带 HTTPS 端口。 |
| 发布设备名 | 与服务器 `publishers.json` 中登记的名称一致，区分大小写。 |
| 发布密钥 | 管理员提供的 64 位小写十六进制原密钥。服务器仅保存其 SHA-256 摘要。地址和设备名保持原值时，可留空沿用已保存的密钥。 |

点击「保存并连接」。应用会验证 HTTPS 证书并用发布凭据读取交付列表，连接通过后才保存。用另一个浏览器窗口打开一条测试链接，验证领取后可在列表撤销。

已用独立管理地址的设备可继续在「高级设置」沿用原地址。切换到 HTTPS 直连时，先让管理员启用同源管理路由，再清空「单独的管理地址」并重新输入发布密钥。清空服务器地址并保存会停用这台设备的交付功能。若要作废已发链接，请先在列表撤销。

## 接口

| 通道 | 路径 | 用途 |
| --- | --- | --- |
| HTTPS 领取 | GET /delivery/s/{id}/ | 打开领取页 |
| HTTPS 领取 | POST /delivery/s/{id}/claim | 用链接密钥和可选口令换取领取会话 |
| HTTPS 领取 | GET /delivery/s/{id}/files/{index} | 下载交付附件 |
| HTTPS 管理 | POST /v1/deliveries | 接收附件与文档 |
| HTTPS 管理 | GET /v1/deliveries | 列出当前发布者的交付 |
| HTTPS 管理 | DELETE /v1/deliveries/{id} | 撤销当前发布者的交付 |

上传时校验附件长度和 SHA-256。默认单交付 16 MiB、总体 32 MiB，Linux 存储卷预留 256 MiB，交付最长保留 7 天。领取密钥位于链接片段，传输时使用 HTTPS 请求正文。会话绑定单个交付，撤销后拒绝新请求。已下载的文件和撤销前已打开的下载连接由收件端控制。交付内容存放在服务数据目录，部署方需限制目录访问并缩短敏感资料保留时间。
