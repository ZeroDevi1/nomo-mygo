import Darwin
import Foundation
import CryptoKit

/// 扩展不运行 TeX 宏或编译器。主应用发布编译快照，PDFKit 在翻页时按需绘制。
enum TypesetPreview {
    struct Snapshot {
        let data: Data
        let caption: String
    }

    static func load(_ source: URL, cacheDirectory: URL) throws -> Snapshot? {
        guard let size = try source.resourceValues(forKeys: [.fileSizeKey]).fileSize, size <= 16 << 20,
              let resolved = realpath(source.path, nil) else { return nil }
        defer { free(resolved) }
        let canonical = String(cString: resolved)
        let handle = try FileHandle(forReadingFrom: source)
        defer { try? handle.close() }
        var hash = SHA256()
        while let chunk = try handle.read(upToCount: 65536), !chunk.isEmpty {
            try Task.checkCancellation()
            hash.update(data: chunk)
        }
        let key = hex(SHA256.hash(data: Data(canonical.utf8))) + "-" + hex(hash.finalize())
        let cache = cacheDirectory.appendingPathComponent(key + ".pdf")
        if let snapshot = pdf(cache, label: "上次编译") { return snapshot }

        // 同名构建产物仅在沙盒允许访问且不早于主文件时使用；日期说明其构建时点。
        let sidecar = source.deletingPathExtension().appendingPathExtension("pdf")
        if let srcDate = try? source.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate,
           let pdfDate = try? sidecar.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate,
           pdfDate >= srcDate {
            return pdf(sidecar, label: sidecar.lastPathComponent)
        }
        return nil
    }

    private static func hex(_ digest: SHA256.Digest) -> String {
        digest.map { String(format: "%02x", $0) }.joined()
    }

    private static func pdf(_ url: URL, label: String) -> Snapshot? {
        guard let attrs = try? url.resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey, .isRegularFileKey]),
              attrs.isRegularFile == true, let size = attrs.fileSize, size > 0, size <= 128 << 20,
              let data = try? Data(contentsOf: url, options: .mappedIfSafe) else { return nil }
        let date = attrs.contentModificationDate.map { DateFormatter.localizedString(from: $0, dateStyle: .short, timeStyle: .short) }
        return Snapshot(data: data, caption: [label, date].compactMap { $0 }.joined(separator: " · "))
    }
}
