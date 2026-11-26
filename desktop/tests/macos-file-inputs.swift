import AppKit
import ApplicationServices
import Foundation

// Run only against the disposable app and Finder fixture owned by the CI probe.
struct ProbeError: Error, CustomStringConvertible { let description: String }
func fail(_ message: String) -> ProbeError { ProbeError(description: message) }
func pause(_ seconds: Double) { Thread.sleep(forTimeInterval: seconds) }

func attribute(_ element: AXUIElement, _ key: String) -> CFTypeRef? {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(element, key as CFString, &value) == .success else { return nil }
    return value
}

func bounds(_ element: AXUIElement) -> CGRect? {
    guard let position = attribute(element, kAXPositionAttribute), CFGetTypeID(position) == AXValueGetTypeID(),
          let size = attribute(element, kAXSizeAttribute), CFGetTypeID(size) == AXValueGetTypeID() else { return nil }
    var point = CGPoint.zero
    var dimensions = CGSize.zero
    guard AXValueGetValue(position as! AXValue, .cgPoint, &point),
          AXValueGetValue(size as! AXValue, .cgSize, &dimensions), dimensions.width > 0, dimensions.height > 0 else { return nil }
    return CGRect(origin: point, size: dimensions)
}

func moveWindow(_ window: AXUIElement, x: CGFloat, width: CGFloat, height: CGFloat) {
    var size = CGSize(width: width, height: height)
    var position = CGPoint(x: x, y: 48)
    if let value = AXValueCreate(.cgSize, &size) { AXUIElementSetAttributeValue(window, kAXSizeAttribute as CFString, value) }
    if let value = AXValueCreate(.cgPoint, &position) { AXUIElementSetAttributeValue(window, kAXPositionAttribute as CFString, value) }
}

func wait<T>(_ label: String, _ read: () -> T?) throws -> T {
    let deadline = Date().addingTimeInterval(15)
    repeat {
        if let value = read() { return value }
        pause(0.15)
    } while Date() < deadline
    let front = NSWorkspace.shared.frontmostApplication
    var visible: [String] = []
    if let pid = front?.processIdentifier {
        let windows = attribute(AXUIElementCreateApplication(pid), kAXWindowsAttribute) as? [AXUIElement] ?? []
        for window in windows.prefix(3) {
            visible.append(attribute(window, kAXTitleAttribute) as? String ?? "(untitled)")
            _ = find(window) { element in
                let role = attribute(element, kAXRoleAttribute) as? String ?? ""
                let title = attribute(element, kAXTitleAttribute) as? String ?? attribute(element, kAXDescriptionAttribute) as? String ?? attribute(element, kAXValueAttribute) as? String ?? ""
                let pickerError = role == "AXStaticText" && ["选择器", "选择文件", "重连", "not allowed", "denied"].contains { title.contains($0) }
                if pickerError || (["AXButton", "AXSheet", "AXTextField"].contains(role) && visible.count < 23) {
                    visible.append(role + " " + title)
                }
                return false
            }
        }
    }
    throw fail("Timed out waiting for " + label + ". Foreground: " + (front?.localizedName ?? "?") + " " + visible.joined(separator: " | "))
}

func firstWindow(_ app: AXUIElement) throws -> AXUIElement {
    try wait("application window") { (attribute(app, kAXWindowsAttribute) as? [AXUIElement])?.first }
}

func find(_ root: AXUIElement, _ matches: (AXUIElement) -> Bool) -> AXUIElement? {
    var pending = [root]
    var index = 0
    while index < pending.count && index < 4000 {
        let element = pending[index]
        index += 1
        if matches(element) { return element }
        if let children = attribute(element, kAXChildrenAttribute) as? [AXUIElement] {
            pending.append(contentsOf: children)
        }
    }
    return nil
}

func control(_ root: AXUIElement, id: String, roles: [String], labels: [String]) throws -> AXUIElement {
    try wait(id) {
        find(root) { element in
            guard let role = attribute(element, kAXRoleAttribute) as? String, roles.contains(role) else { return false }
            let names = ["AXDOMIdentifier", "AXIdentifier", kAXTitleAttribute, kAXDescriptionAttribute,
                         kAXPlaceholderValueAttribute, kAXValueAttribute].compactMap { attribute(element, $0) as? String }
            return names.contains(id) || names.contains(where: labels.contains)
        }
    }
}

func center(_ element: AXUIElement) throws -> CGPoint {
    guard let rect = bounds(element) else { throw fail("Control has no usable screen coordinates") }
    return CGPoint(x: rect.midX, y: rect.midY)
}

func mouse(_ type: CGEventType, _ point: CGPoint) throws {
    guard let event = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: point, mouseButton: .left) else {
        throw fail("Could not construct a mouse event")
    }
    event.post(tap: .cghidEventTap)
}

func key(_ code: CGKeyCode, command: Bool = false, shift: Bool = false) throws {
    for down in [true, false] {
        guard let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: down) else { throw fail("Keyboard event failed") }
        var flags: CGEventFlags = []
        if command { flags.insert(.maskCommand) }
        if shift { flags.insert(.maskShift) }
        event.flags = flags
        event.post(tap: .cghidEventTap)
    }
    pause(0.15)
}

func nativeOpenPanel(_ owner: AXUIElement) -> AXUIElement? {
    if let sheet = (attribute(owner, "AXSheets") as? [AXUIElement])?.first { return sheet }
    return find(owner) { attribute($0, kAXRoleAttribute) as? String == "AXSheet" }
}

func api(_ port: UInt16, _ path: String) throws -> [String: Any] {
    let url = URL(string: "http://127.0.0.1:\(port)/api/v1/" + path)!
    let json = try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any]
    guard let result = json, result["code"] as? Int == 0 else { throw fail("Local backend did not answer " + path) }
    return result
}

func shortcuts(_ port: UInt16) throws -> [String] {
    guard let files = try api(port, "files")["data"] as? [[String: Any]] else { throw fail("Unreadable file index") }
    return files.filter { $0["source_shortcut"] as? Bool == true }.compactMap { $0["file_name"] as? String }
}

func testPicker(_ target: AXUIElement, file: URL, port: UInt16) throws {
    let before = try shortcuts(port)
    for cancel in [true, false] {
        let pick = try control(target, id: "pickShortcutBtn", roles: ["AXButton"], labels: ["选择本机文件"])
        guard AXUIElementPerformAction(pick, kAXPressAction as CFString) == .success else { throw fail("Could not click the file picker") }
        let panel: AXUIElement = try wait("macOS Open sheet attached to OmniShare") { nativeOpenPanel(target) }
        pause(10) // Past the former eight-second API deadline while the user is choosing.
        _ = try api(port, "health")
        guard nativeOpenPanel(target) != nil, try shortcuts(port) == before else {
            throw fail("The picker stopped waiting while the backend was healthy")
        }
        if cancel {
            try key(53) // Escape in the native panel.
            let _: Bool = try wait("picker button after cancelling") {
                attribute(pick, kAXEnabledAttribute) as? Bool == true && nativeOpenPanel(target) == nil ? true : nil
            }
            guard try shortcuts(port) == before else { throw fail("Cancellation registered a file") }
            continue
        }
        NSPasteboard.general.clearContents()
        guard NSPasteboard.general.setString(file.path, forType: .string) else { throw fail("Could not copy the fixture path") }
        try key(5, command: true, shift: true) // Go to Folder in the native open panel.
        let system = AXUIElementCreateSystemWide()
        let entry: AXUIElement = try wait("Go to Folder path field") {
            guard let focused = attribute(system, kAXFocusedUIElementAttribute) else { return nil }
            let field = focused as! AXUIElement
            let role = attribute(field, kAXRoleAttribute) as? String ?? ""
            return ["AXTextField", "AXComboBox", "AXTextArea"].contains(role) ? field : nil
        }
        try key(0, command: true)
        try key(9, command: true)
        let _: Bool = try wait("fixture path in macOS Open panel") {
            (attribute(entry, kAXValueAttribute) as? String)?.contains(file.lastPathComponent) == true ? true : nil
        }
        try key(36)
        let _: Bool = try wait("file confirmation in macOS Open panel") {
            if (try? shortcuts(port).contains(file.lastPathComponent)) == true { return true }
            let button = attribute(panel, kAXDefaultButtonAttribute).map { $0 as! AXUIElement } ?? find(panel) {
                attribute($0, kAXRoleAttribute) as? String == "AXButton" && attribute($0, kAXTitleAttribute) as? String == "选择文件"
            }
            guard let button = button else { return nil }
            guard attribute(button, kAXEnabledAttribute) as? Bool == true else { return nil }
            return AXUIElementPerformAction(button, kAXPressAction as CFString) == .success ? true : nil
        }
        let _: Bool = try wait("exactly one selected file in the backend") {
            guard let names = try? shortcuts(port) else { return nil }
            return names.sorted() == (before + [file.lastPathComponent]).sorted() ? true : nil
        }
    }
}

func probe(pid: pid_t, file: URL, picked: URL, port: UInt16) throws {
    guard AXIsProcessTrusted(), let app = NSRunningApplication(processIdentifier: pid),
          let screen = NSScreen.main else { throw fail("Native input requires Accessibility on the disposable macOS runner") }
    let target = try firstWindow(AXUIElementCreateApplication(pid))
    let width = screen.frame.width
    let split = floor(width * 0.43)
    moveWindow(target, x: split, width: width - split - 10, height: min(screen.frame.height - 110, 750))
    app.activate(options: [.activateIgnoringOtherApps])
    let add = try control(target, id: "dashUploadFileBtn", roles: ["AXButton"], labels: ["添加文件"])
    guard AXUIElementPerformAction(add, kAXPressAction as CFString) == .success else { throw fail("Cannot open Add File") }
    try testPicker(target, file: picked, port: port)
    app.activate(options: [.activateIgnoringOtherApps])
    let reopen = try control(target, id: "openShortcutBtn", roles: ["AXButton"], labels: ["添加本机文件"])
    guard AXUIElementPerformAction(reopen, kAXPressAction as CFString) == .success else { throw fail("Cannot reopen Add File") }
    let path = try control(target, id: "shortcutPath", roles: ["AXTextArea", "AXTextField"],
                           labels: ["本机文件路径", "拖入文件或粘贴完整路径，每行一个文件"])
    let point = try center(path)
    try mouse(.leftMouseDown, point)
    try mouse(.leftMouseUp, point)
    NSPasteboard.general.clearContents()
    guard NSPasteboard.general.setString(file.absoluteString, forType: .string) else { throw fail("Cannot prepare fixture pasteboard") }
    try key(9, command: true) // V
    let _: Bool = try wait("Cmd+V to paste the file URL into OmniShare") {
        attribute(path, kAXValueAttribute) as? String == file.absoluteString ? true : nil
    }
    try key(0, command: true) // A
    try key(51) // Backspace
    let _: Bool = try wait("empty path field before Finder drag") {
        attribute(path, kAXValueAttribute) as? String == "" ? true : nil
    }
    NSWorkspace.shared.activateFileViewerSelecting([file])
    let finder: NSRunningApplication = try wait("Finder") { NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.finder").first }
    let finderAX = AXUIElementCreateApplication(finder.processIdentifier)
    let finderWindow: AXUIElement = try wait("Finder window for fixture directory") {
        let windows = attribute(finderAX, kAXWindowsAttribute) as? [AXUIElement] ?? []
        return windows.first { attribute($0, kAXTitleAttribute) as? String == file.deletingLastPathComponent().lastPathComponent }
    }
    moveWindow(finderWindow, x: 0, width: split - 12, height: min(screen.frame.height - 120, 580))
    let names = [file.lastPathComponent, file.deletingPathExtension().lastPathComponent]
    let icon = try control(finderWindow, id: file.lastPathComponent,
                           roles: ["AXImage", "AXCell", "AXTextField", "AXStaticText", "AXRow", "AXButton"], labels: names)
    let start = try center(icon)
    let end = try center(path)
    guard let frame = bounds(finderWindow), !frame.contains(end) else { throw fail("Finder covers the drop target") }
    try mouse(.mouseMoved, start)
    try mouse(.leftMouseDown, start)
    pause(0.16)
    for i in 1...36 {
        let t = CGFloat(i) / 36
        try mouse(.leftMouseDragged, CGPoint(x: start.x + (end.x - start.x) * t, y: start.y + (end.y - start.y) * t))
        pause(0.025)
    }
    pause(0.3)
    try mouse(.leftMouseUp, end)
    let _: Bool = try wait("Finder drop in the packaged WebView") {
        guard let value = attribute(path, kAXValueAttribute) as? String, !value.isEmpty else { return nil }
        return URL(fileURLWithPath: value).resolvingSymlinksInPath().path == file.resolvingSymlinksInPath().path ? true : nil
    }
    app.activate(options: [.activateIgnoringOtherApps])
    let submit = try control(target, id: "addShortcutBtn", roles: ["AXButton"], labels: ["添加快捷方式"])
    guard AXUIElementPerformAction(submit, kAXPressAction as CFString) == .success else { throw fail("Cannot confirm dropped shortcut") }
}

do {
    guard ProcessInfo.processInfo.environment["GITHUB_ACTIONS"] == "true", CommandLine.arguments.count == 5,
          let pid = Int32(CommandLine.arguments[1]), let port = UInt16(CommandLine.arguments[4]) else {
        throw fail("Expected a disposable CI application PID, fixtures and loopback port")
    }
    try probe(pid: pid, file: URL(fileURLWithPath: CommandLine.arguments[2]),
              picked: URL(fileURLWithPath: CommandLine.arguments[3]), port: port)
    print("PASS native file picker, cancellation, Cmd+V and Finder drag in packaged OmniShare")
} catch {
    FileHandle.standardError.write(Data(("\(error)\n").utf8))
    exit(1)
}
