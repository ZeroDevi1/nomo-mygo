# MyGo 版 Nomo 的 Homebrew cask：本仓库即 tap（brew tap zerodevi1/nomo-mygo https://github.com/ZeroDevi1/nomo-mygo）。
# 发布正式版时 release.yml 的 update-package-manifests 任务更新 version 与 sha256。
cask "nomo-mygo" do
  version "0.7.0"
  sha256 "4caade512e26c6019a3061666c67519454e0147aa1b467e5dfe8ea680aae4466"

  url "https://github.com/ZeroDevi1/nomo-mygo/releases/download/v#{version}/Nomo_#{version}_aarch64.dmg"
  name "Nomo (MyGo)"
  desc "Local-first Markdown editor, MyGo rewrite of Nomo"
  homepage "https://github.com/ZeroDevi1/nomo-mygo"

  livecheck do
    url :url
    strategy :github_latest
  end

  # 与原版 cask 安装的是同名 Nomo.app，共用配置目录。
  conflicts_with cask: "nomo"
  depends_on arch: :arm64
  depends_on macos: :monterey

  app "Nomo.app"

  zap trash: [
    "~/Library/Application Support/com.nomo.desktop",
    "~/Library/Caches/com.nomo.desktop",
    "~/Library/Logs/com.nomo.desktop",
    "~/Library/Preferences/com.nomo.desktop.plist",
  ]
end
