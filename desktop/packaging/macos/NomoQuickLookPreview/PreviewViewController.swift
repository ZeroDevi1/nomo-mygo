import Cocoa
import Darwin
import OSLog
import QuickLookUI

private let appearanceLogger = Logger(
    subsystem: "com.nomo.desktop.quicklook",
    category: "appearance"
)

/// 用原生 AppKit 文本视图展示 Markdown 文件的 Quick Look 预览控制器（渲染见 MarkdownRenderer）。
///
/// 控制器由系统扩展进程通过 `NSExtensionPrincipalClass` 创建；渲染期间只读取传入文件与 Nomo 的
/// 配置、用户主题，不写入任何文件，也不启动 WebKit 进程。无法生成预览时在面板内显示错误文字，
/// 而不是让扩展进程退出。
@objc(PreviewViewController)
final class PreviewViewController: NSViewController, QLPreviewingController {
    /// 正文的只读文本视图（TextKit 1：`NSTextBlock` / `NSTextTable` 需要它）；在 `loadView()` 中创建。
    private var textView: NSTextView!
    private var scrollView: NSScrollView!

    /// 正文最大宽度（pt）：面板更宽时左右留白居中，接近主应用的阅读宽度。
    private let maxContentWidth: CGFloat = 820

    /// 创建铺满 Quick Look 面板的滚动文本视图。由 AppKit 在首次访问 `view` 时调用，不做文件 I/O。
    override func loadView() {
        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.drawsBackground = true
        scroll.borderType = .noBorder

        let storage = NSTextStorage()
        let layout = NSLayoutManager()
        // 只排版可见区域附近的文字，长文档打开不必等全文排版完成。
        layout.allowsNonContiguousLayout = true
        storage.addLayoutManager(layout)
        let container = NSTextContainer(size: NSSize(width: 0, height: CGFloat.greatestFiniteMagnitude))
        container.widthTracksTextView = true
        layout.addTextContainer(container)

        let tv = NSTextView(frame: .zero, textContainer: container)
        tv.isEditable = false
        tv.isSelectable = true
        tv.isRichText = true
        tv.drawsBackground = true
        tv.isVerticallyResizable = true
        tv.isHorizontallyResizable = false
        tv.autoresizingMask = [.width]
        tv.textContainerInset = NSSize(width: 32, height: 28)
        scroll.documentView = tv
        // 设为 documentView 时 maxSize 会被重置为滚动视图当时的尺寸，文本视图高度被锁在面板初始高度、
        // 后文无法滚动显示；须在其后放开上限。
        tv.minSize = .zero
        tv.maxSize = NSSize(width: CGFloat.greatestFiniteMagnitude, height: CGFloat.greatestFiniteMagnitude)

        textView = tv
        scrollView = scroll
        view = scroll
    }

    /// 面板尺寸变化时按最大宽度重新计算左右留白，使正文居中。
    override func viewDidLayout() {
        super.viewDidLayout()
        let width = scrollView.contentSize.width
        let side = max(32, (width - maxContentWidth) / 2)
        if abs(textView.textContainerInset.width - side) > 0.5 {
            textView.textContainerInset = NSSize(width: side, height: 28)
        }
    }

    /// 读取并渲染 Quick Look 提供的 Markdown 文件。
    ///
    /// 读取或渲染失败时显示错误文字并正常完成，避免系统因协议方法抛错而静默回退到其他预览器。
    ///
    /// - Parameter url: 系统授予扩展只读权限的 Markdown 文件 URL；调用期间必须保持可访问。
    /// - Throws: 为满足 `QLPreviewingController` 协议保留；当前实现不向系统抛出。
    @MainActor
    func preparePreviewOfFile(at url: URL) async throws {
        do {
            let markdown = try readMarkdown(url)
            let options = previewOptions()
            let dark = isDark(options.themeMode)
            let palette = ThemePalette(themeID: options.colorThemeID, dark: dark, userThemeJSON: options.userTheme)
            let renderer = MarkdownRenderer(
                palette: palette, fontSize: options.fontSize, lineHeight: options.lineHeight,
                bodyFamily: options.documentFont, monoFamily: options.monoFont,
                classic: options.documentStyleID == "nomo-classic", baseURL: url.deletingLastPathComponent()
            )
            // 解析与富文本构建不触碰视图，放到后台，避免大文档阻塞扩展主线程。大文件只渲染开头（约 1 秒每 MB），
            // 末尾附截断提示。
            let rendered = await Task.detached(priority: .userInitiated) { () -> RenderedText in
                let (shown, truncated) = MarkdownRenderer.truncate(markdown)
                let text = NSMutableAttributedString(attributedString: renderer.render(shown))
                if truncated {
                    text.append(renderer.truncationNote(total: markdown.utf8.count, shown: shown.utf8.count))
                }
                return RenderedText(text)
            }.value.text
            let background = palette.color("documentBackground")
            scrollView.backgroundColor = background
            textView.backgroundColor = background
            textView.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
            textView.linkTextAttributes = [.foregroundColor: palette.color("link"), .cursor: NSCursor.pointingHand]
            textView.selectedTextAttributes = [.backgroundColor: palette.color("selection")]
            textView.textStorage?.setAttributedString(rendered)
        } catch {
            showError(error)
        }
    }

    // MARK: - 读取

    /// 按 UTF-8（含 BOM）、UTF-16 BOM、GB18030 的顺序解码，与主应用支持的编码一致。
    private func readMarkdown(_ url: URL) throws -> String {
        let data = try Data(contentsOf: url)
        if data.starts(with: [0xFF, 0xFE]) || data.starts(with: [0xFE, 0xFF]), let s = String(data: data, encoding: .utf16) {
            return s
        }
        if let s = String(data: data, encoding: .utf8) {
            return s.hasPrefix("\u{FEFF}") ? String(s.dropFirst()) : s
        }
        let gb = CFStringConvertEncodingToNSStringEncoding(CFStringEncoding(CFStringEncodings.GB_18030_2000.rawValue))
        if let s = String(data: data, encoding: String.Encoding(rawValue: gb)) {
            return s
        }
        throw PreviewError.unsupportedEncoding
    }

    /// 预览用到的设置，缺失或不可读时取主应用默认值。
    private struct PreviewOptions {
        var themeMode = "system"
        var colorThemeID = defaultColorThemeID
        var documentStyleID = "nomo-modern"
        var documentFont = ""
        var monoFont = ""
        var fontSize: CGFloat = 16
        var lineHeight: CGFloat = 1.75
        var userTheme: String?
    }

    private func previewOptions() -> PreviewOptions {
        var o = PreviewOptions()
        do {
            let settings = try readPreviewSettings()
            func string(_ key: String) -> String? { (try? readSetting(settings, key)) as? String }
            func number(_ key: String) -> Double? { (try? readSetting(settings, key)) as? Double }
            o.themeMode = string("themeMode") ?? o.themeMode
            o.colorThemeID = string("colorThemeId") ?? o.colorThemeID
            o.documentStyleID = string("documentStyleId") ?? o.documentStyleID
            o.documentFont = string("documentFontFamily") ?? ""
            o.monoFont = string("monoFontFamily") ?? ""
            if let v = number("fontSize"), (12...32).contains(v) { o.fontSize = CGFloat(v) }
            if let v = number("lineHeight"), (1...3).contains(v) { o.lineHeight = CGFloat(v) }
            o.userTheme = readUserThemeSource(o.colorThemeID)
            appearanceLogger.info(
                "Loaded preferences: themeMode=\(o.themeMode, privacy: .public) colorThemeId=\(o.colorThemeID, privacy: .public) documentStyleId=\(o.documentStyleID, privacy: .public)"
            )
        } catch {
            // 配置不可读不应阻断正文预览；使用默认外观，并在统一日志中暴露原因。
            appearanceLogger.error("Could not read preferences: \(String(describing: error), privacy: .public)")
        }
        return o
    }

    /// themeMode 为 light / dark 时固定，system 时跟随面板当前外观。
    private func isDark(_ mode: String) -> Bool {
        switch mode {
        case "dark": return true
        case "light": return false
        default: return view.effectiveAppearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
        }
    }

    /// 读取 Nomo 原生配置中的设置记录。
    ///
    /// 精确的只读沙盒例外由扩展 entitlements 限定到 Nomo 的 Application Support 目录。
    ///
    /// - Returns: 应用配置中的设置记录。
    /// - Throws: 无法定位用户主目录、读取配置或解析配置 JSON 时抛出。
    private func readPreviewSettings() throws -> [String: Any] {
        let configUrl = try resolveUserHomeDirectory()
            .appendingPathComponent("Library/Application Support/com.nomo.desktop", isDirectory: true)
            .appendingPathComponent("config.json", isDirectory: false)
        let data = try Data(contentsOf: configUrl)
        guard
            let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
            let app = root["app"] as? [String: Any],
            let settings = app["settings"] as? [String: Any]
        else {
            throw PreviewError.appearanceConfigurationMalformed
        }
        return settings
    }

    private func readSetting(_ settings: [String: Any], _ key: String) throws -> Any? {
        guard let record = settings[key] as? [String: Any],
              let encodedValue = record["value_json"] as? String,
              let data = encodedValue.data(using: .utf8) else { return nil }
        return try JSONSerialization.jsonObject(with: data, options: .fragmentsAllowed)
    }

    /// 读取当前用户主题的 JSON 源文。
    ///
    /// 只接受与主应用一致的 `user-` 主题 ID 形状，避免把配置值当作路径片段穿越目录；
    /// 文件缺失、过大或不可读时返回 `nil`，随后回退内置主题。
    ///
    /// - Parameter themeId: 配置中的 `colorThemeId`。
    /// - Returns: UTF-8 源文；非用户主题或不可读时为 `nil`。
    private func readUserThemeSource(_ themeId: String) -> String? {
        let pattern = "^user-[a-z0-9]+(-[a-z0-9]+)*$"
        guard themeId.count <= 64,
              themeId.range(of: pattern, options: .regularExpression) != nil,
              let themeUrl = try? resolveUserHomeDirectory()
                  .appendingPathComponent("Library/Application Support/com.nomo.desktop/themes", isDirectory: true)
                  .appendingPathComponent("\(themeId).json", isDirectory: false),
              let attributes = try? FileManager.default.attributesOfItem(atPath: themeUrl.path),
              let size = attributes[.size] as? NSNumber,
              size.intValue <= 256 * 1024,
              let data = try? Data(contentsOf: themeUrl)
        else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    /// 获取 App Sandbox 容器外的真实用户主目录。
    ///
    /// Foundation 的 home-directory API 在沙盒扩展中可能返回扩展容器目录，无法与
    /// home-relative 临时只读例外对应。POSIX 账户记录保留真实主目录，且本方法只用于
    /// 拼接固定的 Nomo 配置相对路径，不接受文档内容或其他外部输入。
    ///
    /// - Returns: 当前有效用户的绝对主目录 URL，已按目录语义创建。
    /// - Throws: 账户记录缺失、主目录不是有效 UTF-8 或返回空路径时抛出 `userHomeUnavailable`。
    private func resolveUserHomeDirectory() throws -> URL {
        guard
            let passwordEntry = getpwuid(getuid()),
            let homePath = String(validatingUTF8: passwordEntry.pointee.pw_dir),
            !homePath.isEmpty
        else {
            throw PreviewError.userHomeUnavailable
        }
        return URL(fileURLWithPath: homePath, isDirectory: true)
    }

    /// 在文本视图中显示预览错误（纯文本，不解释任何标记）。
    private func showError(_ error: Error) {
        let para = NSMutableParagraphStyle()
        para.alignment = .center
        let text = NSMutableAttributedString(string: "无法生成 Nomo 预览\n", attributes: [
            .font: NSFont.boldSystemFont(ofSize: 16), .foregroundColor: NSColor.labelColor, .paragraphStyle: para,
        ])
        text.append(NSAttributedString(string: String(describing: error), attributes: [
            .font: NSFont.systemFont(ofSize: 13), .foregroundColor: NSColor.secondaryLabelColor, .paragraphStyle: para,
        ]))
        textView.textStorage?.setAttributedString(text)
    }
}

/// 把后台新建的富文本交回主线程。富文本在后台任务内创建、只移交一次且之后不再被后台访问，
/// 因此跨线程传递是安全的（`NSAttributedString` 本身未标注 Sendable）。
private final class RenderedText: @unchecked Sendable {
    let text: NSAttributedString
    init(_ text: NSAttributedString) { self.text = text }
}

/// Quick Look 渲染准备阶段可向内部调用方报告的确定性错误。
private enum PreviewError: Error, CustomStringConvertible {
    case unsupportedEncoding
    case userHomeUnavailable
    case appearanceConfigurationMalformed

    /// 面向 Quick Look 用户的简短中文错误说明，不包含文件内容或路径等敏感信息。
    var description: String {
        switch self {
        case .unsupportedEncoding:
            return "无法识别文件编码"
        case .userHomeUnavailable:
            return "无法定位用户主目录"
        case .appearanceConfigurationMalformed:
            return "Nomo 外观配置格式无效"
        }
    }
}
