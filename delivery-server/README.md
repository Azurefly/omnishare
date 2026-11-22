# OmniShare 公开交付服务

公开交付服务是可独立部署的 Go 程序。收件人在浏览器打开领取链接即可查看文档或下载指定附件。OmniShare 通过独立管理通道发布和撤销交付。未配置交付服务器的设备会禁用公开交付。

## 部署

1. 使用 Go 1.26 构建服务。在 PowerShell 中从项目根目录执行：

   \`\`\`powershell
   go -C delivery-server test ./...
   $env:GOOS = 'linux'
   $env:GOARCH = 'amd64'
   $env:CGO_ENABLED = '0'
   go -C delivery-server build -trimpath -o ../omnishare-delivery ./cmd/omnishare-delivery
   Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
   \`\`\`

2. 在 Linux 上将二进制安装至 /opt/omnishare-delivery/omnishare-delivery。将 deploy/omnishare-delivery.service 安装到 /etc/systemd/system/，在 /etc/omnishare-delivery/server.env 设置 PUBLIC_ORIGIN，值是收件人打开的 HTTPS 源地址。
3. 给每个发布设备生成独立的 32 字节随机密钥，使用小写十六进制保存这 64 个字符。系统管理员通过安全通道将密钥交给发布端。服务器的 /etc/omnishare-delivery/publishers.json 仅保存设备名与这 64 个 ASCII 字符的 SHA-256 小写十六进制摘要。将配置文件权限设为 0600。
4. 运行 systemctl daemon-reload 和 systemctl enable --now omnishare-delivery。配置具有受信证书的 HTTPS 反向代理，仅将 /delivery/ 转发到 127.0.0.1:18484。可参考 [Nginx 示例](deploy/nginx.conf.example)。通过 GET /delivery/health 验证 204 响应。
5. 为发布端建立到 127.0.0.1:18485 的 SSH 转发。Windows PowerShell 可运行 ssh -N -L 127.0.0.1:18485:127.0.0.1:18485 -o ExitOnForwardFailure=yes DEPLOY_USER@DELIVERY_HOST。用实际服务器地址替换 DELIVERY_HOST，提前校验 SSH 主机公钥。管理端口保持仅回环监听。
6. 在 OmniShare 打开「公开交付 → 配置交付服务器」，填写公网 HTTPS 地址、管理地址 http://127.0.0.1:18485、发布设备名和密钥。不同设备使用各自凭据。

## 接口

| 通道 | 路径 | 用途 |
| --- | --- | --- |
| HTTPS 领取 | GET /delivery/s/{id}/ | 打开领取页 |
| HTTPS 领取 | POST /delivery/s/{id}/claim | 用链接密钥和可选口令换取领取会话 |
| HTTPS 领取 | GET /delivery/s/{id}/files/{index} | 下载交付附件 |
| 独立管理 | POST /v1/deliveries | 接收附件与文档 |
| 独立管理 | GET /v1/deliveries | 列出当前发布者的交付 |
| 独立管理 | DELETE /v1/deliveries/{id} | 撤销当前发布者的交付 |

上传时校验附件长度和 SHA-256。默认单交付 16 MiB、总体 32 MiB，Linux 存储卷预留 256 MiB，交付最长保留 7 天。领取密钥位于链接片段，传输时使用 HTTPS 请求正文。会话绑定单个交付，撤销后拒绝新请求。已下载的文件和撤销前已打开的下载连接由收件端控制。交付内容存放在服务数据目录，部署方需限制目录访问并缩短敏感资料保留时间。
