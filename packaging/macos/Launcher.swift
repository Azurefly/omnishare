import AppKit
import Darwin
import Foundation

// The app owns one backend and one private stdin pipe. Closing that pipe asks
// Go to drain HTTP requests and flush SQLite; no public shutdown endpoint exists.
final class OmniShareApplication: NSObject, NSApplicationDelegate {
    private var backend: Process?
    private var control: Pipe?
    private var log: FileHandle?
    private var timer: Timer?
    private var statusItem: NSStatusItem?
    private var dataDirectory: URL?
    private var serviceURL: URL?
    private var ready = false
    private var stopping = false
    private var fallbackUsed = false
    private var probeInFlight = false
    private var deadline = Date.distantPast
    private var suppressBrowser = false
    private var suppressReopenBrowser = false

    private func option(_ name: String, in arguments: [String]) -> String? {
        for (index, argument) in arguments.enumerated() {
            if argument == name, index + 1 < arguments.count { return arguments[index + 1] }
            if argument.hasPrefix(name + "=") { return String(argument.dropFirst(name.count + 1)) }
        }
        return nil
    }

    private func savedConfiguration(in directory: URL) -> [String: Any] {
        // Match Go's recovery order: an absent or malformed primary file must
        // not hide the valid configuration backup before Go can restore it.
        for name in ["config.json", "config.json.bak"] {
            if let bytes = try? Data(contentsOf: directory.appendingPathComponent(name)),
               let value = try? JSONSerialization.jsonObject(with: bytes) as? [String: Any] {
                return value
            }
        }
        // Go remains responsible for validation and reports an error when both
        // existing files are corrupt; the launcher never writes replacement data.
        return [:]
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        configureMenus()
        do { try startBackend() }
        catch { fail("无法启动 OmniShare。请检查数据目录是否可写，以及是否已有另一个实例正在使用它。") }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if ready && !suppressReopenBrowser { openWorkspace(nil) }
        return true
    }

    private func configureMenus() {
        let menu = NSMenu()
        let appItem = NSMenuItem()
        let appMenu = NSMenu(title: "OmniShare")
        let openItem = NSMenuItem(title: "打开 OmniShare", action: #selector(openWorkspace(_:)), keyEquivalent: "o")
        openItem.target = self
        appMenu.addItem(openItem)
        appMenu.addItem(.separator())
        appMenu.addItem(NSMenuItem(title: "退出 OmniShare", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
        appItem.submenu = appMenu
        menu.addItem(appItem)
        NSApplication.shared.mainMenu = menu

        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        item.button?.title = "OmniShare"
        item.button?.toolTip = "打开或退出 OmniShare"
        item.menu = appMenu.copy() as? NSMenu
        statusItem = item
    }

    private func available(_ port: Int) -> Bool {
        guard (1024...65535).contains(port) else { return false }
        let descriptor = Darwin.socket(AF_INET, SOCK_STREAM, 0)
        guard descriptor >= 0 else { return false }
        defer { Darwin.close(descriptor) }
        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = UInt16(port).bigEndian
        address.sin_addr.s_addr = inet_addr("127.0.0.1")
        return withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(descriptor, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) == 0
            }
        }
    }

    private func startBackend() throws {
        let arguments = Array(CommandLine.arguments.dropFirst()).filter { !$0.hasPrefix("-psn_") }
        let environment = ProcessInfo.processInfo.environment
        let path = option("--data-dir", in: arguments) ?? environment["OMNISHARE_DATA_DIR"]
            ?? (NSHomeDirectory() as NSString).appendingPathComponent(".omnishare")
        let directory = URL(fileURLWithPath: path, isDirectory: true).standardizedFileURL
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                               attributes: [.posixPermissions: 0o700])
        dataDirectory = directory
        let config = savedConfiguration(in: directory)
        let explicitPort = option("--port", in: arguments) ?? environment["OMNISHARE_PORT"]
        let configured = (config["port"] as? Int) ?? 8081
        let preferred = (1024...65535).contains(configured) ? configured : 8081
        var port = preferred
        if let explicitPort = explicitPort {
            guard let requested = Int(explicitPort), (1...65535).contains(requested) else {
                throw NSError(domain: "OmniShare", code: 2)
            }
            port = requested
        } else {
            // Persist the chosen port through Go's normal config path. An
            // explicitly supplied port remains strict and produces a visible
            // error if unavailable rather than silently changing a deployment.
            guard let free = (preferred...min(preferred + 31, 65535)).first(where: available)
                    ?? (49152...65535).first(where: available) else {
                throw NSError(domain: "OmniShare", code: 1)
            }
            port = free
        }
        guard (1...65535).contains(port) else { throw NSError(domain: "OmniShare", code: 2) }
        let certificate = option("--tls-cert", in: arguments) ?? (config["tls_cert_file"] as? String ?? "")
        let tls = !certificate.isEmpty
        serviceURL = URL(string: "\(tls ? "https" : "http")://127.0.0.1:\(port)")
        suppressBrowser = arguments.contains("--no-browser") || (config["auto_open_browser"] as? Bool == false)
        suppressReopenBrowser = arguments.contains("--no-browser")

        let executable = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/omnishare-server")
        guard FileManager.default.isExecutableFile(atPath: executable.path) else {
            throw NSError(domain: "OmniShare", code: 3)
        }
        let logPath = directory.appendingPathComponent("desktop-launcher.log").path
        let descriptor = Darwin.open(logPath, O_WRONLY | O_APPEND | O_CREAT | O_CLOEXEC | O_NOFOLLOW, 0o600)
        guard descriptor >= 0, fchmod(descriptor, 0o600) == 0 else {
            if descriptor >= 0 { Darwin.close(descriptor) }
            throw NSError(domain: "OmniShare", code: 4)
        }
        log = FileHandle(fileDescriptor: descriptor, closeOnDealloc: true)
        let pipe = Pipe()
        let process = Process()
        process.executableURL = executable
        process.currentDirectoryURL = directory
        process.arguments = arguments + ["--data-dir", directory.path, "--port", String(port),
                                          "--no-browser", "--shutdown-on-stdin-close"]
        process.standardInput = pipe
        process.standardOutput = log
        process.standardError = log
        process.terminationHandler = { [weak self] completed in
            DispatchQueue.main.async {
                guard let self = self else { return }
                self.timer?.invalidate()
                self.ready = false
                if self.stopping {
                    // Keep a private exit record so diagnostics and native
                    // acceptance can distinguish a drained exit from timeout
                    // fallback. It contains no user content or credentials.
                    if let bytes = try? JSONSerialization.data(withJSONObject: [
                        "backend_pid": completed.processIdentifier,
                        "exit_code": completed.terminationStatus,
                        "normal": completed.terminationReason == .exit && completed.terminationStatus == 0 && !self.fallbackUsed,
                        "method": self.fallbackUsed ? "signal-fallback" : "stdin-eof",
                        "fallback_used": self.fallbackUsed
                    ]) {
                        let record = directory.appendingPathComponent("desktop-shutdown.json")
                        try? bytes.write(to: record, options: .atomic)
                        try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: record.path)
                    }
                    NSApplication.shared.reply(toApplicationShouldTerminate: true)
                } else {
                    self.fail("本地服务已停止（状态 \(completed.terminationStatus)）。请检查端口或数据目录是否被另一个实例占用。诊断日志保存在数据目录的 desktop-launcher.log。")
                }
            }
        }
        control = pipe
        backend = process
        try process.run()
        pipe.fileHandleForReading.closeFile()
        deadline = Date().addingTimeInterval(45)
        timer = Timer.scheduledTimer(withTimeInterval: 0.2, repeats: true) { [weak self] _ in self?.probe() }
    }

    private func probe() {
        guard !stopping, !ready, let process = backend, process.isRunning, let base = serviceURL else { return }
        if Date() >= deadline {
            fail("本地服务未在预期时间内准备完成。请检查数据目录中的 desktop-launcher.log。")
            return
        }
        guard !probeInFlight else { return }
        probeInFlight = true
        var request = URLRequest(url: base.appendingPathComponent("api/v1/health"))
        request.timeoutInterval = 2
        request.cachePolicy = .reloadIgnoringLocalCacheData
        URLSession.shared.dataTask(with: request) { [weak self] data, response, _ in
            DispatchQueue.main.async {
                guard let self = self else { return }
                self.probeInFlight = false
                guard !self.stopping, process.isRunning,
                      (response as? HTTPURLResponse)?.statusCode == 200,
                      let data = data,
                      let payload = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                      let status = payload["data"] as? [String: Any], status["status"] as? String == "ok",
                      let version = status["version"] as? String, !version.isEmpty,
                      version.split(separator: "-").first.map(String.init) == Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
                      let directory = self.dataDirectory,
                      let lock = try? String(contentsOf: directory.appendingPathComponent(".omnishare.lock"), encoding: .utf8),
                      lock == "pid=\(process.processIdentifier)\n" else { return }
                self.ready = true
                self.timer?.invalidate()
                if !self.suppressBrowser { self.openWorkspace(nil) }
            }
        }.resume()
    }

    @objc private func openWorkspace(_ sender: Any?) {
        guard ready, let base = serviceURL else { return }
        var target = URLComponents(url: base, resolvingAgainstBaseURL: false)!
        if let directory = dataDirectory {
            let tokenURL = directory.appendingPathComponent("init-token")
            if let attributes = try? FileManager.default.attributesOfItem(atPath: tokenURL.path),
               attributes[.type] as? FileAttributeType == .typeRegular,
               let permissions = attributes[.posixPermissions] as? NSNumber, permissions.intValue & 0o077 == 0,
               let token = try? String(contentsOf: tokenURL, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines),
               token.count >= 32, token.count <= 512, !token.contains(where: { $0.isWhitespace }) {
                target.queryItems = [URLQueryItem(name: "init_token", value: token)]
            }
        }
        if let url = target.url, !NSWorkspace.shared.open(url) {
            let alert = NSAlert()
            alert.messageText = "无法打开默认浏览器"
            alert.informativeText = "请在浏览器中打开 \(base.absoluteString)。"
            alert.runModal()
        }
    }

    private func fail(_ message: String) {
        guard !stopping else { return }
        timer?.invalidate()
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = "OmniShare 无法继续运行"
        alert.informativeText = message
        alert.addButton(withTitle: "退出")
        NSApplication.shared.activate(ignoringOtherApps: true)
        alert.runModal()
        NSApplication.shared.terminate(nil)
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        timer?.invalidate()
        guard let process = backend, process.isRunning else { return .terminateNow }
        if stopping { return .terminateLater }
        stopping = true
        // EOF is the ordinary exit path on every platform. Fallback signals
        // are scoped to the Process we spawned and are only used after timeout.
        control?.fileHandleForWriting.closeFile()
        control = nil
        DispatchQueue.main.asyncAfter(deadline: .now() + 12) {
            if process.isRunning { self.fallbackUsed = true; process.terminate() }
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 17) {
            if process.isRunning { self.fallbackUsed = true; Darwin.kill(process.processIdentifier, SIGKILL) }
        }
        return .terminateLater
    }

    func applicationWillTerminate(_ notification: Notification) {
        control?.fileHandleForWriting.closeFile()
        log?.closeFile()
    }
}

let application = NSApplication.shared
let delegate = OmniShareApplication()
application.delegate = delegate
application.setActivationPolicy(.regular)
application.run()
