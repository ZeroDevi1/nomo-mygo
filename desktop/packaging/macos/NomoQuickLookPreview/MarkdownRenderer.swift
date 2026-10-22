import Cocoa

/// Quick Look 的原生 Markdown 渲染：用 Foundation 的 `AttributedString(markdown:)` 解析
/// （CommonMark + GFM 表格、删除线），把 `PresentationIntent` 映射为 AppKit 富文本——标题、
/// 列表、引用与提示块（`NSTextBlock` 背景与左边框）、代码块、表格（`NSTextTable`）、分隔线、
/// 行内样式、链接与本地图片。配色取自与主应用相同的主题令牌。
///
/// 与主应用的差异：公式与 Mermaid 以源码显示；front matter 以等宽文字块显示在开头。
/// 渲染器无状态，`render` 每次返回新的富文本，可在任意线程调用（不触碰视图）。
struct MarkdownRenderer {
    /// 主题令牌解析后的颜色；缺失的令牌回退到默认主题同一明暗变体。
    let palette: ThemePalette
    /// 正文字号（pt），与主应用 fontSize 设置一致（14–22）。
    let fontSize: CGFloat
    /// 行高倍数（lineHeight 设置，1.4–2.2），作用于正文段落。
    let lineHeight: CGFloat
    /// 正文与等宽字体族名；空串使用系统字体。
    let bodyFamily: String
    let monoFamily: String
    /// 经典块样式（documentStyleId = nomo-classic）：引用与提示块使用 *ClassicBackground。
    let classic: Bool
    /// 文档所在目录，用于解析相对图片地址；沙盒拒绝读取时图片以替代文字显示。
    let baseURL: URL?

    /// 渲染完整 Markdown 源文（可为空）。
    func render(_ source: String) -> NSAttributedString {
        let out = NSMutableAttributedString()
        var body = source
        if let (front, rest) = splitFrontMatter(source) {
            appendFrontMatter(front, to: out)
            body = rest
        }
        let options = AttributedString.MarkdownParsingOptions(
            allowsExtendedAttributes: true,
            interpretedSyntax: .full,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        body = preprocess(body)
        guard let parsed = try? AttributedString(markdown: body, options: options, baseURL: baseURL) else {
            out.append(NSAttributedString(string: body, attributes: [.font: bodyFont(), .foregroundColor: palette.color("foreground")]))
            return out
        }
        var state = RenderState()
        for block in groupBlocks(parsed) {
            renderBlock(block, in: parsed, state: &state, to: out)
        }
        // 去掉末尾多余换行，避免底部空白。
        while out.length > 0, out.string.hasSuffix("\n") {
            out.deleteCharacters(in: NSRange(location: out.length - 1, length: 1))
        }
        return out
    }

    // MARK: - 分块

    /// 一个块：连续且 `PresentationIntent` 相同的若干 run（如一个段落、一个代码块、一个表格单元格）。
    private struct Block {
        var components: [PresentationIntent.IntentType] // 由外到内
        var ranges: [Range<AttributedString.Index>]
    }

    private func groupBlocks(_ text: AttributedString) -> [Block] {
        var blocks: [Block] = []
        var lastKey: [Int]? = nil
        for run in text.runs {
            let comps = Array((run.presentationIntent?.components ?? []).reversed())
            let key = comps.map(\.identity)
            if key == lastKey, !blocks.isEmpty {
                blocks[blocks.count - 1].ranges.append(run.range)
            } else {
                blocks.append(Block(components: comps, ranges: [run.range]))
                lastKey = key
            }
        }
        return blocks
    }

    /// 跨块共享的渲染状态：同一引用 / 代码块 / 表格的段落共用同一个 `NSTextBlock` / `NSTextTable`，
    /// 才会画成一个连续的框；列表项只在首段显示标记。
    private struct RenderState {
        var quoteBlocks: [Int: NSTextBlock] = [:]
        var calloutTones: [Int: Int?] = [:]
        var tables: [Int: TableState] = [:]
        var startedItems: Set<Int> = []
    }

    private final class TableState {
        let table: NSTextTable
        var rowIdentity = -1
        var row = -1
        var nextColumn = 0
        init(columns: Int) {
            table = NSTextTable()
            table.numberOfColumns = max(1, columns)
            table.collapsesBorders = true
            table.layoutAlgorithm = .automaticLayoutAlgorithm
            table.setValue(100, type: .percentageValueType, for: .width)
        }
    }

    // MARK: - 块渲染

    private func renderBlock(_ block: Block, in text: AttributedString, state: inout RenderState, to out: NSMutableAttributedString) {
        let para = NSMutableParagraphStyle()
        para.lineHeightMultiple = lineHeight
        para.paragraphSpacing = fontSize * 0.75
        var textBlocks: [NSTextBlock] = []
        var font = bodyFont()
        var color = palette.color("foreground")
        var listDepth = 0
        var marker: String? = nil
        var isCode = false
        var calloutTitle = false // 本块是提示块的首段：首行的 [!TYPE] 换成标题
        var headingLevel = 0
        var tableCell: (state: TableState, column: Int, header: Bool)? = nil
        var thematic = false

        // 由外到内应用各层意图。
        for (i, comp) in block.components.enumerated() {
            switch comp.kind {
            case .blockQuote:
                let tone = calloutTone(quoteID: comp.identity, block: block, text: text, state: &state)
                if state.quoteBlocks[comp.identity] == nil {
                    state.quoteBlocks[comp.identity] = quoteBlock(tone: tone)
                    calloutTitle = tone != nil
                }
                textBlocks.append(state.quoteBlocks[comp.identity]!)
                color = tone == nil ? palette.color("blockquoteForeground") : palette.toneText(tone!)
            case .orderedList, .unorderedList:
                listDepth += 1
            case .listItem(let ordinal):
                if !state.startedItems.contains(comp.identity) {
                    state.startedItems.insert(comp.identity)
                    let ordered = i > 0 && { if case .orderedList = block.components[i - 1].kind { return true } else { return false } }()
                    marker = ordered ? "\(ordinal)." : (listDepth > 1 ? "◦" : "•")
                }
            case .header(let level):
                headingLevel = level
            case .codeBlock:
                // 代码、公式与 Mermaid 都以源码显示（不内置高亮与图表渲染）。
                isCode = true
            case .table(let columns):
                if state.tables[comp.identity] == nil {
                    state.tables[comp.identity] = TableState(columns: columns.count)
                }
            case .tableHeaderRow, .tableRow:
                if let tid = block.components.prefix(i).last(where: { if case .table = $0.kind { return true } else { return false } })?.identity,
                   let ts = state.tables[tid], ts.rowIdentity != comp.identity {
                    // 新的一行：先补齐上一行缺失的空单元格。
                    if ts.row >= 0 { fillCells(ts, upTo: ts.table.numberOfColumns, header: false, to: out) }
                    ts.rowIdentity = comp.identity
                    ts.row += 1
                    ts.nextColumn = 0
                }
            case .tableCell(let column):
                if let tid = block.components.prefix(i).last(where: { if case .table = $0.kind { return true } else { return false } })?.identity,
                   let ts = state.tables[tid] {
                    let header = block.components.contains { if case .tableHeaderRow = $0.kind { return true } else { return false } }
                    fillCells(ts, upTo: column, header: header, to: out)
                    tableCell = (ts, column, header)
                }
            case .thematicBreak:
                thematic = true
            default:
                break
            }
        }

        if thematic {
            let rule = fullWidthBlock()
            rule.setWidth(1, type: .absoluteValueType, for: .border, edge: .minY)
            rule.setBorderColor(palette.color("border"), for: .minY)
            rule.setWidth(fontSize * 0.6, type: .absoluteValueType, for: .margin, edge: .minY)
            rule.setWidth(fontSize * 0.6, type: .absoluteValueType, for: .margin, edge: .maxY)
            para.textBlocks = textBlocks + [rule]
            out.append(NSAttributedString(string: "\u{200B}\n", attributes: [.font: NSFont.systemFont(ofSize: 2), .paragraphStyle: para]))
            return
        }

        // 标题与代码的字体、颜色、间距。
        if headingLevel > 0 {
            let sizes: [CGFloat] = [2.0, 1.625, 1.375, 1.2, 1.05, 1.0]
            font = bodyFont(size: fontSize * sizes[min(headingLevel, 6) - 1], bold: true)
            color = palette.color("heading")
            para.paragraphSpacingBefore = fontSize * (headingLevel <= 2 ? 1.0 : 0.6)
            para.paragraphSpacing = fontSize * 0.5
            para.lineHeightMultiple = 1.2
        }
        if isCode {
            font = monoFont(size: fontSize * 0.875)
            color = palette.color("codeForeground")
            para.lineHeightMultiple = 1.35
            para.paragraphSpacing = 0
            let box = fullWidthBlock()
            box.backgroundColor = palette.color("codeBackground")
            box.setWidth(1, type: .absoluteValueType, for: .border)
            box.setBorderColor(palette.color("codeBorder"))
            box.setWidth(12, type: .absoluteValueType, for: .padding)
            box.setWidth(fontSize * 0.6, type: .absoluteValueType, for: .margin, edge: .maxY)
            textBlocks.append(box)
        }

        // 列表缩进：标记放在悬挂缩进里，正文与制表位对齐。
        if listDepth > 0 {
            let indent = CGFloat(listDepth) * fontSize * 1.6
            para.headIndent = indent
            para.firstLineHeadIndent = indent
            para.tabStops = [NSTextTab(textAlignment: .left, location: indent)]
            para.paragraphSpacing = fontSize * 0.3
            if marker != nil { para.firstLineHeadIndent = indent - fontSize * 1.4 }
        }

        if let cell = tableCell {
            let cb = NSTextTableBlock(table: cell.state.table, startingRow: cell.state.row, rowSpan: 1, startingColumn: cell.column, columnSpan: 1)
            styleCell(cb, header: cell.header)
            para.textBlocks = textBlocks + [cb]
            para.paragraphSpacing = 0
            para.lineHeightMultiple = 1.2
            if cell.header { font = bodyFont(bold: true) }
            cell.state.nextColumn = cell.column + 1
        } else {
            para.textBlocks = textBlocks
        }

        // 行内内容。
        let content = NSMutableAttributedString()
        if let marker {
            content.append(NSAttributedString(string: marker + "\t", attributes: [.font: font, .foregroundColor: palette.color("mutedForeground")]))
        }
        for range in block.ranges {
            content.append(inline(text[range], baseFont: font, color: color, code: isCode))
        }
        if isCode, content.string.hasSuffix("\n") {
            content.deleteCharacters(in: NSRange(location: content.length - 1, length: 1))
        }
        replaceTaskMarker(in: content, enabled: marker != nil)
        if calloutTitle { stripCalloutMarker(in: content) }
        content.append(NSAttributedString(string: "\n", attributes: [.font: font]))
        if content.containsAttachments(in: NSRange(location: 0, length: content.length)) {
            // 行高倍数也会放大图片所在行，含图片的段落按 1 倍行高。
            para.lineHeightMultiple = 1
        }
        content.addAttribute(.paragraphStyle, value: para, range: NSRange(location: 0, length: content.length))
        out.append(content)
    }

    /// 在 upTo 列之前补齐当前行缺失的单元格（解析器不会为空单元格产生 run）。
    private func fillCells(_ ts: TableState, upTo column: Int, header: Bool, to out: NSMutableAttributedString) {
        while ts.nextColumn < column {
            let cb = NSTextTableBlock(table: ts.table, startingRow: max(0, ts.row), rowSpan: 1, startingColumn: ts.nextColumn, columnSpan: 1)
            styleCell(cb, header: header)
            let para = NSMutableParagraphStyle()
            para.textBlocks = [cb]
            out.append(NSAttributedString(string: "\n", attributes: [.font: bodyFont(), .paragraphStyle: para]))
            ts.nextColumn += 1
        }
    }

    private func styleCell(_ cb: NSTextTableBlock, header: Bool) {
        cb.setWidth(1, type: .absoluteValueType, for: .border)
        cb.setBorderColor(palette.color("tableBorder"))
        cb.setWidth(fontSize * 0.5, type: .absoluteValueType, for: .padding)
        if header { cb.backgroundColor = palette.color("tableHeaderBackground") }
    }

    // MARK: - 引用与提示块

    private static let inlineTagPattern = #"</?(u|mark|kbd|strong|b|em|i|s|del|ins|sup|sub|span|small|br|code|a)(\s[^<>]*)?/?>"#

    private static let calloutTypes = ["NOTE", "TIP", "IMPORTANT", "WARNING", "CAUTION"]

    /// 引用块首段以 `[!NOTE]` 等开头时为提示块，返回语气序号（0–4），普通引用返回 nil。
    private func calloutTone(quoteID: Int, block: Block, text: AttributedString, state: inout RenderState) -> Int? {
        if let cached = state.calloutTones[quoteID] { return cached }
        let first = block.ranges.first.map { String(text[$0].characters) } ?? ""
        var tone: Int? = nil
        if first.hasPrefix("[!"), let end = first.firstIndex(of: "]") {
            let name = first[first.index(first.startIndex, offsetBy: 2)..<end].uppercased()
            tone = Self.calloutTypes.firstIndex(of: name)
        }
        state.calloutTones[quoteID] = tone
        return tone
    }

    private func quoteBlock(tone: Int?) -> NSTextBlock {
        let b = fullWidthBlock()
        let bg = tone == nil
            ? palette.color(classic ? "blockquoteClassicBackground" : "blockquoteBackground")
            : palette.toneBackground(tone!, classic: classic)
        b.backgroundColor = bg
        b.setWidth(4, type: .absoluteValueType, for: .border, edge: .minX)
        b.setBorderColor(tone == nil ? palette.color("blockquoteBorder") : palette.toneBorder(tone!), for: .minX)
        b.setWidth(fontSize * 0.9, type: .absoluteValueType, for: .padding, edge: .minX)
        b.setWidth(fontSize * 0.6, type: .absoluteValueType, for: .padding, edge: .maxX)
        b.setWidth(fontSize * 0.4, type: .absoluteValueType, for: .padding, edge: .minY)
        b.setWidth(fontSize * 0.1, type: .absoluteValueType, for: .padding, edge: .maxY)
        b.setWidth(fontSize * 0.6, type: .absoluteValueType, for: .margin, edge: .maxY)
        return b
    }

    /// 把提示块首行的 `[!NOTE] 标题` 换成粗体标题（无标题时用类型名）。
    private func stripCalloutMarker(in content: NSMutableAttributedString) {
        let s = content.string as NSString
        guard s.hasPrefix("[!"), case let end = s.range(of: "]"), end.location != NSNotFound, end.location < 16 else { return }
        let name = s.substring(with: NSRange(location: 2, length: end.location - 2)).uppercased()
        guard let tone = Self.calloutTypes.firstIndex(of: name) else { return }
        var cut = end.location + 1
        if cut < s.length, "+-".contains(s.substring(with: NSRange(location: cut, length: 1))) { cut += 1 }
        let lineEnd = s.range(of: "\n", range: NSRange(location: cut, length: s.length - cut)).location
        let titleRange = NSRange(location: cut, length: (lineEnd == NSNotFound ? s.length : lineEnd) - cut)
        var title = s.substring(with: titleRange).trimmingCharacters(in: .whitespaces)
        if title.isEmpty { title = name.prefix(1) + name.dropFirst().lowercased() }
        let attrs: [NSAttributedString.Key: Any] = [.font: bodyFont(bold: true), .foregroundColor: palette.toneText(tone)]
        content.replaceCharacters(in: NSRange(location: 0, length: titleRange.location + titleRange.length),
                                  with: NSAttributedString(string: title, attributes: attrs))
    }

    /// 任务列表项的 `[ ]` / `[x]` 换成复选框符号。
    private func replaceTaskMarker(in content: NSMutableAttributedString, enabled: Bool) {
        guard enabled else { return }
        let s = content.string as NSString
        let tab = s.range(of: "\t")
        guard tab.location != NSNotFound else { return }
        let start = tab.location + 1
        guard start + 3 <= s.length else { return }
        let box = s.substring(with: NSRange(location: start, length: 3))
        let symbol: String
        switch box {
        case "[ ]": symbol = "☐"
        case "[x]", "[X]": symbol = "☑"
        default: return
        }
        content.replaceCharacters(in: NSRange(location: start, length: 3), with: symbol)
    }

    // MARK: - 行内

    private func inline(_ run: AttributedSubstring, baseFont: NSFont, color: NSColor, code: Bool) -> NSAttributedString {
        let out = NSMutableAttributedString()
        for r in run.runs {
            var str = String(run[r.range].characters)
            var font = baseFont
            var attrs: [NSAttributedString.Key: Any] = [.foregroundColor: color]
            if let intent = r.inlinePresentationIntent, !code {
                if intent.contains(.stronglyEmphasized) { font = NSFontManager.shared.convert(font, toHaveTrait: .boldFontMask) }
                if intent.contains(.emphasized) { font = NSFontManager.shared.convert(font, toHaveTrait: .italicFontMask) }
                if intent.contains(.strikethrough) { attrs[.strikethroughStyle] = NSUnderlineStyle.single.rawValue }
                if intent.contains(.code) {
                    font = monoFont(size: baseFont.pointSize * 0.9)
                    attrs[.foregroundColor] = palette.color("codeForeground")
                    attrs[.backgroundColor] = palette.color("codeBackground")
                }
            }
            if let url = r.imageURL {
                out.append(image(url: url, alt: str, font: font))
                continue
            }
            if let link = r.link {
                attrs[.link] = link
            }
            if !code, r.inlinePresentationIntent?.contains(.code) != true {
                // 解析器把行内 HTML 当作文字；去掉常见的格式标签，保留其中文字。
                str = str.replacingOccurrences(of: Self.inlineTagPattern, with: "", options: .regularExpression)
                if str.isEmpty { continue }
            }
            attrs[.font] = font
            out.append(NSAttributedString(string: str, attributes: attrs))
        }
        return out
    }

    /// 本地图片按最大 640pt 宽缩放内嵌；远程或无法读取的图片显示替代文字。
    private func image(url: URL, alt: String, font: NSFont) -> NSAttributedString {
        if url.isFileURL, let img = NSImage(contentsOf: url), img.size.width > 0 {
            let scale = min(1, 640 / img.size.width)
            let attachment = NSTextAttachment()
            attachment.image = img
            attachment.bounds = CGRect(x: 0, y: 0, width: img.size.width * scale, height: img.size.height * scale)
            return NSAttributedString(attachment: attachment)
        }
        let label = "🖼 " + (alt.isEmpty ? url.lastPathComponent : alt)
        return NSAttributedString(string: label, attributes: [.font: font, .foregroundColor: palette.color("mutedForeground"), .link: url])
    }

    /// 解析前的逐行预处理（只作用于围栏代码块以外）：
    /// - 去掉整行的 HTML 注释（如 `<!-- toc -->`、markdownlint 配置注释）：解析器会把它们当作正文，
    ///   而主应用不显示注释；
    /// - 提示块的 `> [!TYPE] 标题` 行后补一个空引用行，使标题自成一段（解析器不保留软换行，
    ///   否则标题与下一行正文会连成一行）；
    /// - 以 HTML 标签开头的行（README 常见的 `<p align>`、`<img>`、`<h1>`）转成 Markdown：图片、链接、
    ///   标题保留，其余标签去掉只留文字，空行丢弃。
    private func preprocess(_ source: String) -> String {
        var fence: String? = nil
        var out: [Substring] = []
        for line in source.split(separator: "\n", omittingEmptySubsequences: false) {
            let t = line.trimmingCharacters(in: .whitespaces)
            if let f = fence {
                if t.hasPrefix(f) { fence = nil }
            } else if t.hasPrefix("```") || t.hasPrefix("~~~") {
                fence = String(t.prefix(3))
            } else if t.hasPrefix("<!--"), t.hasSuffix("-->") {
                continue
            } else if t.range(of: #"^</?[A-Za-z][A-Za-z0-9-]*(\s|>|/|$)"#, options: .regularExpression) != nil {
                let md = htmlLineToMarkdown(t)
                if !md.trimmingCharacters(in: .whitespaces).isEmpty { out.append(Substring(md)) }
                continue
            } else if t.range(of: #"^>\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\][+-]?"#, options: [.regularExpression, .caseInsensitive]) != nil {
                out.append(line)
                out.append(">")
                continue
            }
            out.append(line)
        }
        return out.joined(separator: "\n")
    }

    /// 把一行 HTML 转成近似的 Markdown（见 preprocess）。
    private func htmlLineToMarkdown(_ line: String) -> String {
        func attr(_ tag: String, _ name: String) -> String? {
            guard let r = tag.range(of: name + #"\s*=\s*"([^"]*)""#, options: .regularExpression) else { return nil }
            let m = String(tag[r])
            return m.firstIndex(of: "\"").map { String(m[m.index(after: $0)...].dropLast()) }
        }
        var s = line
        // 图片与链接。
        s = replace(s, #"<img\b[^>]*>"#) { tag in
            guard let src = attr(tag, "src") else { return "" }
            return "![\((attr(tag, "alt") ?? "").replacingOccurrences(of: "]", with: " "))](<\(src)>)"
        }
        s = replace(s, #"<a\b[^>]*>(.*?)</a>"#) { tag in
            let text = tag.replacingOccurrences(of: #"<[^>]+>"#, with: "", options: .regularExpression)
            guard let href = attr(tag, "href") else { return text }
            return "[\(text)](<\(href)>)"
        }
        // 标题。
        var prefix = ""
        if let r = s.range(of: #"<h([1-6])\b"#, options: .regularExpression), let n = Int(s[r].dropFirst(2)) {
            prefix = String(repeating: "#", count: n) + " "
        }
        s = s.replacingOccurrences(of: #"<br\s*/?>"#, with: "  ", options: .regularExpression)
        s = s.replacingOccurrences(of: #"</?[A-Za-z][^>]*>"#, with: "", options: .regularExpression)
        s = s.trimmingCharacters(in: .whitespaces)
        return s.isEmpty ? "" : prefix + s
    }

    private func replace(_ s: String, _ pattern: String, _ transform: (String) -> String) -> String {
        guard let re = try? NSRegularExpression(pattern: pattern, options: [.caseInsensitive]) else { return s }
        let ns = s as NSString
        var out = ""
        var last = 0
        for m in re.matches(in: s, range: NSRange(location: 0, length: ns.length)) {
            out += ns.substring(with: NSRange(location: last, length: m.range.location - last))
            out += transform(ns.substring(with: m.range))
            last = m.range.location + m.range.length
        }
        return out + ns.substring(from: last)
    }

    // MARK: - front matter

    private func splitFrontMatter(_ source: String) -> (String, String)? {
        guard source.hasPrefix("---\n") || source.hasPrefix("---\r\n") else { return nil }
        let lines = source.components(separatedBy: "\n")
        for i in 1..<lines.count where ["---", "..."].contains(lines[i].trimmingCharacters(in: .whitespacesAndNewlines)) {
            let front = lines[1..<i].joined(separator: "\n")
            let rest = lines[(i + 1)...].joined(separator: "\n")
            return (front, rest)
        }
        return nil
    }

    private func appendFrontMatter(_ front: String, to out: NSMutableAttributedString) {
        guard !front.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let box = fullWidthBlock()
        box.backgroundColor = palette.color("surface")
        box.setWidth(1, type: .absoluteValueType, for: .border)
        box.setBorderColor(palette.color("border"))
        box.setWidth(12, type: .absoluteValueType, for: .padding)
        box.setWidth(fontSize, type: .absoluteValueType, for: .margin, edge: .maxY)
        let para = NSMutableParagraphStyle()
        para.textBlocks = [box]
        para.lineHeightMultiple = 1.3
        out.append(NSAttributedString(string: front + "\n", attributes: [
            .font: monoFont(size: fontSize * 0.8), .foregroundColor: palette.color("mutedForeground"), .paragraphStyle: para,
        ]))
    }

    /// 撑满可用宽度的文字块：NSTextBlock 不设宽度时按内容收缩到最小宽度，会把正文挤成一列。
    private func fullWidthBlock() -> NSTextBlock {
        let b = NSTextBlock()
        b.setValue(100, type: .percentageValueType, for: .width)
        return b
    }

    // MARK: - 字体

    func bodyFont(size: CGFloat? = nil, bold: Bool = false) -> NSFont {
        let size = size ?? fontSize
        var font = family(bodyFamily, size: size) ?? NSFont.systemFont(ofSize: size)
        if bold { font = NSFontManager.shared.convert(font, toHaveTrait: .boldFontMask) }
        return font
    }

    func monoFont(size: CGFloat) -> NSFont {
        family(monoFamily, size: size) ?? NSFont.monospacedSystemFont(ofSize: size, weight: .regular)
    }

    /// 按设置中的字体族列表（逗号分隔，取第一个已安装的）创建字体。
    private func family(_ list: String, size: CGFloat) -> NSFont? {
        for name in list.split(separator: ",") {
            let n = name.trimmingCharacters(in: CharacterSet(charactersIn: " \"'"))
            if n.isEmpty { continue }
            if let f = NSFontManager.shared.font(withFamily: n, traits: [], weight: 5, size: size) ?? NSFont(name: n, size: size) {
                return f
            }
        }
        return nil
    }
}

/// 主题令牌到 `NSColor` 的解析：内置主题来自 BuiltinThemes.swift，用户主题的覆盖项合并到其
/// baseThemeId 之上；无法解析的值回退默认主题。
struct ThemePalette {
    private let tokens: [String: String]
    private let fallback: [String: String]

    init(themeID: String, dark: Bool, userThemeJSON: String?) {
        let scheme = dark ? "dark" : "light"
        let builtin = (try? JSONSerialization.jsonObject(with: Data(builtinThemeTokensJSON.utf8))) as? [String: [String: [String: String]]] ?? [:]
        fallback = builtin[defaultColorThemeID]?[scheme] ?? [:]
        var t = builtin[themeID]?[scheme] ?? fallback
        if let json = userThemeJSON,
           let root = (try? JSONSerialization.jsonObject(with: Data(json.utf8))) as? [String: Any] {
            let base = root["baseThemeId"] as? String ?? defaultColorThemeID
            t = builtin[base]?[scheme] ?? fallback
            if let variants = root["variants"] as? [String: Any],
               let v = variants[scheme] as? [String: Any],
               let overrides = v["tokens"] as? [String: String] {
                t.merge(overrides) { _, new in new }
            }
        }
        tokens = t
    }

    func color(_ name: String) -> NSColor {
        parseCSSColor(tokens[name]) ?? parseCSSColor(fallback[name]) ?? .textColor
    }

    private static let tones = ["Note", "Tip", "Important", "Warning", "Caution"]
    func toneBorder(_ i: Int) -> NSColor { color("callout\(Self.tones[i])Border") }
    func toneText(_ i: Int) -> NSColor { color("callout\(Self.tones[i])Foreground") }
    func toneBackground(_ i: Int, classic: Bool) -> NSColor {
        color("callout\(Self.tones[i])\(classic ? "ClassicBackground" : "Background")")
    }
}

/// 解析 #rgb、#rrggbb、#rrggbbaa、rgb()/rgba()、transparent；其他（CSS 函数等）返回 nil。
func parseCSSColor(_ value: String?) -> NSColor? {
    guard var v = value?.trimmingCharacters(in: .whitespaces).lowercased(), !v.isEmpty else { return nil }
    if v == "transparent" { return .clear }
    if v.hasPrefix("#") {
        v.removeFirst()
        if v.count == 3 { v = v.map { "\($0)\($0)" }.joined() }
        guard v.count == 6 || v.count == 8, let n = UInt64(v, radix: 16) else { return nil }
        let a = v.count == 8 ? CGFloat(n & 0xff) / 255 : 1
        let rgb = v.count == 8 ? n >> 8 : n
        return NSColor(srgbRed: CGFloat((rgb >> 16) & 0xff) / 255, green: CGFloat((rgb >> 8) & 0xff) / 255,
                       blue: CGFloat(rgb & 0xff) / 255, alpha: a)
    }
    if v.hasPrefix("rgb"), let open = v.firstIndex(of: "("), let close = v.lastIndex(of: ")") {
        let parts = v[v.index(after: open)..<close].split { ",/ ".contains($0) }.map(String.init)
        guard parts.count >= 3 else { return nil }
        let c = parts.prefix(3).compactMap { Double($0) }
        guard c.count == 3 else { return nil }
        var a = 1.0
        if parts.count > 3 {
            let p = parts[3]
            a = p.hasSuffix("%") ? (Double(p.dropLast()) ?? 100) / 100 : (Double(p) ?? 1)
        }
        return NSColor(srgbRed: c[0] / 255, green: c[1] / 255, blue: c[2] / 255, alpha: max(0, min(1, a)))
    }
    return nil
}
