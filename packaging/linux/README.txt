OmniShare v__VERSION__ Linux

运行 ./install.sh 完成用户级安装。程序文件位于 ~/.local/lib/omnishare，数据位于 ${XDG_DATA_HOME:-~/.local/share}/omnishare。卸载不会删除数据。
新安装默认开启局域网（0.0.0.0）和设备自动发现，密钥保护默认关闭。可在设置中自行开启并设置任意非空密钥，无长度及字符类型要求；不需要初始化 Token。关闭保护后可连接本机的设备能读取、修改、删除内容和配置，请勿暴露到公网。升级保留已有网络与密钥设置。
