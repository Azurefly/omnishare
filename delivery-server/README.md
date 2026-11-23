# OmniShare 公开交付服务

公开交付服务是可独立部署的 Go 程序。收件人在浏览器打开领取链接即可查看文档或下载指定附件。OmniShare 通过独立管理通道发布和撤销交付。未配置交付服务器的设备会禁用公开交付。

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
4. 运行 systemctl daemon-reload 和 systemctl enable --now omnishare-delivery。配置具有受信证书的 HTTPS 反向代理，仅将 /delivery/ 转发到 127.0.0.1:18484。可参考 [Nginx 示例](deploy/nginx.conf.example)。通过 GET /delivery/health 验证 204 响应。
## 在 OmniShare 中配置

如果服务已部署，请向管理员获取公网 HTTPS 根地址、管理通道连接方式、为本设备登记的发布设备名，以及对应的 64 字符原密钥。

### 1. 建立管理连接

管理员提供了私有 HTTPS 管理地址时，可直接在应用填写该地址。使用 SSH 转发时，请向管理员确认 SSH 用户、主机、端口和主机指纹。在 Windows 11 的 PowerShell 执行以下命令，将示例中的登录目标换成管理员提供的目标。

~~~powershell
ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18485:127.0.0.1:18485 DEPLOY_USER@DELIVERY_HOST
~~~

如果要指定私钥，在登录目标前加上 `-i "C:\路径\私钥文件"`。非默认 SSH 端口用 `-p 端口号`。首次连接时，对照管理员提供的主机指纹后再继续。连接成功后终端通常没有输出，保持它运行。Windows 重启或关闭隧道后，发布、刷新列表及撤销前需要重新连接。

### 2. 填写四项

在这台设备的 OmniShare 中打开「公开交付 → 配置交付服务器」。

| 字段 | 应填内容 |
| --- | --- |
| 公网领取地址 | 收件人可访问的 HTTPS 根地址，与服务端的 `PUBLIC_ORIGIN` 一致。示例 `https://delivery.example.com`，非默认 HTTPS 端口可写成 `https://delivery.example.com:8443`。填写到域名或 IP 和端口为止。 |
| 发布管理地址 | SSH 隧道使用 `http://127.0.0.1:18485`。若本机改用了其他转发端口，填对应端口。也可填写管理员提供的私有 HTTPS 管理地址。 |
| 发布设备名 | 管理员登记在服务端 `publishers.json` 中的设备名，区分大小写。示例 `office-pc`。 |
| 发布密钥 | 管理员交付的 64 位小写十六进制原密钥。发布端填写原密钥，服务器保存其 SHA-256 摘要。已配置且其余三项不变时，留空会沿用已保存的密钥。 |

### 3. 验证连接

1. 保存后，在「公开交付」点「刷新」。出现「交付列表已刷新」表示管理连接和发布凭据有效。没有交付记录时，列表显示「尚未发布交付」。
2. 从文件、随手记或协同文档发布一条测试交付，用另一个浏览器窗口打开领取链接，验证公网领取。验证后在「公开交付」列表撤销测试链接。
3. 如果刷新提示管理通道不可用，先确认 SSH 仍在运行、本机转发端口与管理地址一致，再与管理员核对发布设备名和原密钥。领取页打不开时，核对 HTTPS 根地址与浏览器证书提示。

清空公网领取地址并保存会停用这台设备的公开交付。要使已经发出的链接失效，请先在列表撤销。

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
