# MyGo 版 Nomo 的 Homebrew cask：本仓库即 tap（brew tap zerodevi1/nomo-mygo https://github.com/ZeroDevi1/nomo-mygo）。
# 发布正式版时 release.yml 的 update-package-manifests 任务更新 version 与 sha256。
cask "nomo-mygo" do
  version "0.9.1"
  sha256 "1b0229c03e9ec66a68d733a7dca060fe39e728e46c01a855e4ff478b1e3f1a98"

  url "https://github.com/ZeroDevi1/nomo-mygo/releases/download/v#{version}/Nomo_#{version}_aarch64.dmg"
  name "Nomo Scholar"
  desc "Local-first editor for Markdown, LaTeX, and Typst"
  homepage "https://github.com/ZeroDevi1/nomo-mygo"

  livecheck do
    url :url
    strategy :github_latest
  end

  # 与原版共用配置和恢复目录。当前发行物的 .app 名称随 release.yml 一起更新。
  conflicts_with cask: "nomo"
  depends_on arch: :arm64
  depends_on macos: :monterey

  app "Nomo Scholar.app"

  zap trash: [
    "~/Library/Application Support/com.nomo.desktop",
    "~/Library/Caches/com.nomo.desktop",
    "~/Library/Logs/com.nomo.desktop",
    "~/Library/Preferences/com.nomo.desktop.plist",
  ]
end
