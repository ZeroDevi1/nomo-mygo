package main

import "nomo/desktop/internal/emoji"

// load_emoji_catalog 返回 Emoji 原始目录数据，下载、校验与缓存见 desktop/internal/emoji。
func init() {
	register("load_emoji_catalog", func(_ *commandCall, _ noArgs) (*emoji.Payload, error) {
		return emoji.LoadPayload(appIdentifier)
	})
}
