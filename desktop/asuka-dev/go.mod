module nomo/desktop/asuka-dev

go 1.27.3

require (
	github.com/egoist/asuka v0.1.0
	nomo v0.0.0
)

require (
	github.com/ebitengine/purego v0.11.2 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)

replace nomo => ../..

tool github.com/egoist/asuka/cmd/asuka
